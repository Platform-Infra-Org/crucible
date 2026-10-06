package gitsync

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"maps"
	"path/filepath"
	"regexp"
	"sync"
	"sync/atomic"
	"time"

	"crucible/internal/config"
	"crucible/internal/content"
)

type State struct {
	Platform    *config.Platform
	PlatformSHA string
	PlatformErr string                       // last platform config error; config stays at PlatformSHA
	Trainings   map[string]*content.Training // "id@sha" → valid training
	Heads       map[string]string            // training id → tracked branch HEAD
	ProgramSHAs map[string]string            // "team/training" → sha the program runs
	Problems    map[string][]content.Problem // "id@sha" → why that version is invalid
	SyncedAt    time.Time

	validated map[string]bool // keys whose Problems come from content validation (permanent), not transient export errors
}

func (s *State) Training(id, sha string) *content.Training {
	if s == nil {
		return nil
	}
	return s.Trainings[id+"@"+sha]
}

func (s *State) ProgramTraining(team, training string) (*content.Training, string) {
	sha := s.ProgramSHAs[team+"/"+training]
	return s.Training(training, sha), sha
}

type Syncer struct {
	DataDir, PlatformRepo, PlatformBranch string
	Log                                   *slog.Logger
	// OnProblem, if set, is called for every problem key that is new compared with the previous sync. The first sync
	// after start never calls it, so a restart does not re-announce old failures. Keys: "platform", "<training>",
	// "<training>@<sha>", "<team>/<training>". It runs inside SyncOnce and must not call SyncOnce.
	OnProblem func(key string, problems []content.Problem)

	cur     atomic.Pointer[State]
	mu      sync.Mutex
	trigger chan struct{}
}

func New(dataDir, platformRepo, platformBranch string, log *slog.Logger) *Syncer {
	return &Syncer{DataDir: dataDir, PlatformRepo: platformRepo, PlatformBranch: platformBranch, Log: log, trigger: make(chan struct{}, 1)}
}

// Current returns the latest state, or nil before the first successful sync.
func (s *Syncer) Current() *State { return s.cur.Load() }

// Trigger requests an immediate sync (git webhook).
func (s *Syncer) Trigger() {
	select {
	case s.trigger <- struct{}{}:
	default:
	}
}

func (s *Syncer) Run(ctx context.Context, every time.Duration) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(every):
		case <-s.trigger:
		}
		if err := s.SyncOnce(ctx); err != nil {
			s.Log.Error("git sync failed", "err", err)
		}
	}
}

func (s *Syncer) mirror(url string) Mirror {
	h := sha256.Sum256([]byte(url))
	return Mirror{URL: url, Dir: filepath.Join(s.DataDir, "mirrors", hex.EncodeToString(h[:8]))}
}

func (s *Syncer) SyncOnce(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	prev := s.cur.Load()

	pm := s.mirror(s.PlatformRepo)
	if err := pm.Fetch(ctx); err != nil {
		return err
	}
	psha, err := pm.Resolve(ctx, s.PlatformBranch)
	if err != nil {
		return err
	}
	pdir := filepath.Join(s.DataDir, "platform", psha)
	if err := pm.Export(ctx, psha, pdir); err != nil {
		return err
	}
	plat, err := config.Load(pdir)
	if err != nil {
		// Keep serving the last good config; surface the error on Forge Status.
		next := State{PlatformErr: err.Error()}
		if prev != nil {
			next = *prev
			next.PlatformErr = err.Error()
		}
		if prev != nil && prev.PlatformErr != err.Error() && s.OnProblem != nil {
			s.OnProblem("platform", []content.Problem{{File: "platform", Msg: err.Error()}})
		}
		s.cur.Store(&next)
		return fmt.Errorf("platform config at %.7s: %w", psha, err)
	}

	st := &State{Platform: plat, PlatformSHA: psha, Trainings: map[string]*content.Training{},
		Heads: map[string]string{}, ProgramSHAs: map[string]string{}, Problems: map[string][]content.Problem{}, SyncedAt: time.Now(), validated: map[string]bool{}}
	if prev != nil {
		// ponytail: every version ever loaded stays in memory so running labs keep their content after a pin bump.
		// Ceiling: memory grows with commits; prune versions with no program and no live lab if it ever matters.
		maps.Copy(st.Trainings, prev.Trainings)
	}

	for id, ref := range plat.Trainings {
		m := s.mirror(ref.Repo)
		if err := m.Fetch(ctx); err != nil {
			st.Problems[id] = []content.Problem{{File: ref.Repo, Msg: err.Error()}}
			s.keepPrevPrograms(st, prev, plat, id)
			continue
		}
		head, err := m.Resolve(ctx, ref.Branch)
		if err != nil {
			st.Problems[id] = []content.Problem{{File: ref.Repo, Msg: err.Error()}}
			s.keepPrevPrograms(st, prev, plat, id)
			continue
		}
		st.Heads[id] = head
		s.load(ctx, st, prev, m, id, head)
		for teamID, team := range plat.Teams {
			p, ok := team.Programs[id]
			if !ok {
				continue
			}
			key := teamID + "/" + id
			sha := head
			if p.PinnedRef != "" {
				if sha, err = m.Resolve(ctx, p.PinnedRef); err != nil {
					st.Problems[key] = []content.Problem{{File: "teams/" + teamID + "/programs/" + id + ".yaml", Msg: err.Error()}}
					if old, ok := prev.programSHA(key); ok {
						st.ProgramSHAs[key] = old
					}
					continue
				}
				s.load(ctx, st, prev, m, id, sha)
			}
			st.ProgramSHAs[key] = sha
			if st.Training(id, sha) == nil && prev != nil && prev.Training(id, prev.ProgramSHAs[key]) != nil {
				st.ProgramSHAs[key] = prev.ProgramSHAs[key] // invalid new version: stay on the last good one
			}
		}
	}
	if prev != nil && s.OnProblem != nil {
		for key, probs := range st.Problems {
			if _, seen := prev.Problems[key]; !seen {
				s.OnProblem(key, probs)
			}
		}
	}
	s.cur.Store(st)
	return nil
}

func (s *Syncer) keepPrevPrograms(st, prev *State, plat *config.Platform, id string) {
	if prev == nil {
		return
	}
	for teamID := range plat.Teams {
		if sha, ok := prev.ProgramSHAs[teamID+"/"+id]; ok {
			st.ProgramSHAs[teamID+"/"+id] = sha
		}
	}
}

func (s *Syncer) load(ctx context.Context, st, prev *State, m Mirror, id, sha string) {
	key := id + "@" + sha
	if st.Trainings[key] != nil || st.Problems[key] != nil {
		return
	}
	if prev != nil && prev.validated[key] && prev.Problems[key] != nil {
		st.validated[key] = true
		st.Problems[key] = prev.Problems[key]
		return
	}
	dir := filepath.Join(s.DataDir, "content", id, sha)
	if err := m.Export(ctx, sha, dir); err != nil {
		st.Problems[key] = []content.Problem{{File: id, Msg: err.Error()}}
		return
	}
	t, probs := content.Load(dir)
	if t != nil && t.ID != id {
		probs, t = append(probs, content.Problem{File: "training.yaml", Msg: fmt.Sprintf("id %q does not match registry id %q", t.ID, id)}), nil
	}
	if len(probs) > 0 {
		st.Problems[key] = probs
		st.validated[key] = true
		s.Log.Warn("training version rejected", "training", id, "sha", sha[:7], "problems", len(probs))
		return
	}
	st.Trainings[key] = t
}

func (s *State) programSHA(key string) (string, bool) {
	if s == nil {
		return "", false
	}
	sha, ok := s.ProgramSHAs[key]
	return sha, ok
}

var commitSHA = regexp.MustCompile(`^[0-9a-f]{40}$`)

// Version returns training id at sha, exporting it from the mirror (which keeps full history) when it is not in
// memory, e.g. a lab started on a version that a restart dropped. nil if the training is unregistered or that version
// is invalid; the failure is recorded in Problems until the next sync, which retries it on the next call.
func (s *Syncer) Version(ctx context.Context, id, sha string) *content.Training {
	if t := s.Current().Training(id, sha); t != nil || !commitSHA.MatchString(sha) {
		return t // only a full commit id: never a ref that moves, a path, or something git would read as an option
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	cur := s.cur.Load()
	if cur == nil || cur.Platform == nil {
		return nil
	}
	key := id + "@" + sha
	ref, ok := cur.Platform.Trainings[id]
	if !ok || cur.Trainings[key] != nil || cur.Problems[key] != nil {
		return cur.Training(id, sha)
	}
	next := *cur
	next.Trainings, next.Problems, next.validated = maps.Clone(cur.Trainings), maps.Clone(cur.Problems), maps.Clone(cur.validated)
	s.load(ctx, &next, cur, s.mirror(ref.Repo), id, sha)
	s.cur.Store(&next)
	return next.Training(id, sha)
}
