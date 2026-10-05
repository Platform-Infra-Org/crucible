package configapi

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"crucible/internal/apperr"
	"crucible/internal/auth"
	"crucible/internal/db/dbtest"
	"crucible/internal/gitsync"
)

func sh(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@x"}, args...)...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// bareFrom copies src into a fresh repo and returns a bare clone of it; rewrite edits files before the commit.
func bareFrom(t *testing.T, src string, rewrite map[string]string) string {
	t.Helper()
	work := t.TempDir()
	if out, err := exec.Command("cp", "-R", src+"/.", work).CombinedOutput(); err != nil {
		t.Fatalf("cp: %v %s", err, out)
	}
	for rel, body := range rewrite {
		_ = os.WriteFile(filepath.Join(work, rel), []byte(body), 0o644)
	}
	sh(t, work, "init", "-q", "-b", "main")
	sh(t, work, "add", "-A")
	sh(t, work, "commit", "-qm", "seed")
	out := filepath.Join(t.TempDir(), "r.git")
	sh(t, "", "clone", "-q", "--bare", work, out)
	return out
}

type fx struct {
	s                              *Service
	sync                           *gitsync.Syncer
	remote                         string
	admin, leader, senior, trainee *auth.User
}

func setup(t *testing.T) *fx {
	t.Helper()
	content := bareFrom(t, "../../examples/forge-101", nil)
	remote := bareFrom(t, "../../examples/platform", map[string]string{
		"trainings.yaml": "trainings:\n  forge-101: {repo: " + content + "}\n  forge-201: {repo: " + content + "}\n"})
	syncer := gitsync.New(t.TempDir(), remote, "main", slog.Default())
	if err := syncer.SyncOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	pool := dbtest.New(t)
	s := &Service{DB: pool, State: syncer.Current, Resync: syncer.SyncOnce,
		Writer: &gitsync.Writer{URL: remote, Branch: "main", Dir: filepath.Join(t.TempDir(), "w"), Name: "Crucible", Email: "bot@x"}}
	u := func(e string) *auth.User { return &auth.User{Email: e} }
	return &fx{s: s, sync: syncer, remote: remote, admin: u("admin@crucible.local"), leader: u("leader@crucible.local"),
		senior: u("senior@crucible.local"), trainee: u("trainee@crucible.local")}
}

func (f *fx) sha() string { return f.sync.Current().PlatformSHA }

func emptyProgram(base string) ProgramBody {
	return ProgramBody{BaseSHA: base, Roles: RolesView{Manager: []string{}, Scorers: []string{}, Approvers: []string{}}}
}

func TestLeaderEnrollsTheTeamAndATrainee(t *testing.T) {
	ctx := context.Background()
	f := setup(t)
	view, err := f.s.Team(f.leader, "forge")
	if err != nil || !view.CanEditTeam || len(view.AvailableTrainings) != 1 || view.AvailableTrainings[0].ID != "forge-201" {
		t.Fatalf("team view %+v %v", view, err)
	}
	if _, err := f.s.SetProgram(ctx, f.leader, "forge", "forge-201", emptyProgram(f.sha())); err != nil {
		t.Fatal(err)
	}
	body := emptyProgram(f.sha())
	body.Enrolled = []string{"TRAINEE@crucible.local"}
	body.LabDefaults = LabDefaultsView{TTL: "90m"}
	sha, err := f.s.SetProgram(ctx, f.leader, "forge", "forge-201", body)
	if err != nil {
		t.Fatal(err)
	}
	st := f.sync.Current()
	prog := st.Platform.Teams["forge"].Programs["forge-201"]
	if st.PlatformSHA != sha || prog == nil || prog.Enrolled[0] != "trainee@crucible.local" || prog.LabDefaults.TTL.D().Minutes() != 90 {
		t.Fatalf("resynced program %+v at %s (want %s)", prog, st.PlatformSHA, sha)
	}
	log := sh(t, f.remote, "log", "--format=%B", "-2", "main")
	if !strings.Contains(log, "crucible: enroll forge in forge-201 by leader@crucible.local") ||
		!strings.Contains(log, "crucible: update program forge/forge-201 by leader@crucible.local") || !strings.Contains(log, "Crucible-Actor: leader@crucible.local") {
		t.Fatalf("commits:\n%s", log)
	}
	var action, commit string
	_ = f.s.DB.QueryRow(ctx, `SELECT action, commit_sha FROM audit_log ORDER BY id DESC LIMIT 1`).Scan(&action, &commit)
	if action != "program.update" || commit != sha {
		t.Fatalf("audit %s %s", action, commit)
	}
}

func TestPermissions(t *testing.T) {
	ctx := context.Background()
	f := setup(t)
	if _, err := f.s.SetProgram(ctx, f.trainee, "forge", "forge-101", emptyProgram(f.sha())); !errors.Is(err, apperr.Forbidden) {
		t.Fatalf("trainee edits a program: %v", err)
	}
	if _, err := f.s.SetRoster(ctx, f.senior, "forge", RosterBody{BaseSHA: f.sha()}); !errors.Is(err, apperr.Forbidden) {
		t.Fatalf("senior edits the roster: %v", err)
	}
	if _, err := f.s.SetBudget(ctx, f.leader, "forge", BudgetBody{BaseSHA: f.sha(), MonthlyUSD: 1}); !errors.Is(err, apperr.Forbidden) {
		t.Fatalf("leader edits the team budget: %v", err)
	}
	if _, err := f.s.SetBudget(ctx, f.admin, "forge", BudgetBody{BaseSHA: f.sha(), MonthlyUSD: 300, HardCapUSD: 350}); err != nil {
		t.Fatal(err)
	}
	if b := f.sync.Current().Platform.Teams["forge"].Budget; b.MonthlyUSD != 300 || b.HardCapUSD != 350 {
		t.Fatalf("budget %+v", b)
	}
	if _, err := f.s.Team(&auth.User{Email: "stranger@x"}, "forge"); !errors.Is(err, apperr.NotFound) {
		t.Fatalf("outsiders: %v", err)
	}
	if teams, _ := f.s.Teams(f.admin); len(teams) != 1 || teams[0].Role != "admin" {
		t.Fatalf("admins see every team: %+v", teams)
	}
	if _, err := f.s.Platform(f.leader); !errors.Is(err, apperr.Forbidden) {
		t.Fatalf("platform view is admin-only: %v", err)
	}
}

func TestStaleEditIsRefused(t *testing.T) {
	ctx := context.Background()
	f := setup(t)
	old := f.sha()
	body := emptyProgram(old)
	body.Enrolled = []string{"trainee@crucible.local"}
	body.Schedule = "business-hours"
	if _, err := f.s.SetProgram(ctx, f.leader, "forge", "forge-101", body); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.SetProgram(ctx, f.leader, "forge", "forge-101", body); !errors.Is(err, apperr.Conflict) || !strings.Contains(err.Error(), "Someone changed this in git, reload") {
		t.Fatalf("second save from the same stale page: %v", err)
	}
	roster := RosterBody{BaseSHA: old, Seniors: []string{"senior@crucible.local"}, Trainees: []string{"trainee@crucible.local"},
		Mentors: map[string]string{"trainee@crucible.local": "senior@crucible.local"}}
	if _, err := f.s.SetRoster(ctx, f.leader, "forge", roster); err != nil {
		t.Fatalf("a different file from the same page is fine: %v", err)
	}
}

func TestInvalidEditIsExplained(t *testing.T) {
	ctx := context.Background()
	f := setup(t)
	roster := RosterBody{BaseSHA: f.sha(), Seniors: []string{"senior@crucible.local"}, Trainees: []string{"trainee@crucible.local"},
		Mentors: map[string]string{"trainee@crucible.local": "trainee@crucible.local"}}
	if _, err := f.s.SetRoster(ctx, f.leader, "forge", roster); !errors.Is(err, apperr.Invalid) || !strings.Contains(err.Error(), "mentor pairing") {
		t.Fatalf("invalid mentor: %v", err)
	}
	body := emptyProgram(f.sha())
	body.LabDefaults = LabDefaultsView{TTL: "forever"}
	if _, err := f.s.SetProgram(ctx, f.leader, "forge", "forge-101", body); !errors.Is(err, apperr.Invalid) {
		t.Fatalf("bad duration: %v", err)
	}
	if _, err := f.s.SetProgram(ctx, f.leader, "forge", "../../etc", emptyProgram(f.sha())); !errors.Is(err, apperr.NotFound) {
		t.Fatalf("unknown training id: %v", err)
	}
}
