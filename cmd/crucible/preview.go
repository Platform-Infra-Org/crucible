package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"crucible/internal/agent"
	"crucible/internal/content"
	"crucible/internal/yamlx"
)

// docker runs the docker CLI; tests swap it for a fake.
var docker = func(out io.Writer, args ...string) error {
	cmd := exec.Command("docker", args...)
	cmd.Stdout, cmd.Stderr = out, out
	return cmd.Run()
}

// preview runs the real Crucible image (API + Postgres) against a git snapshot of an author's working tree (spec §6):
// readings, quizzes and local labs with their setup scripts, re-synced within seconds of every save. Only files git
// tracks (or that are staged) are snapshotted, so an untracked .env never reaches the preview.
func preview(args []string) int {
	flags := flag.NewFlagSet("preview", flag.ExitOnError)
	port := flags.Int("port", 8090, "local port for the preview (bound to 127.0.0.1)")
	image := flags.String("image", "crucible:dev", "Crucible image (build it with: docker build -t crucible:dev .)")
	free := flags.Bool("free", false, "preview with progression: free so every module is open")
	_ = flags.Parse(args)
	dir := flags.Arg(0)
	if flags.NArg() > 0 {
		_ = flags.Parse(flags.Args()[1:]) // flags may also follow the directory
	}
	if dir == "" || flags.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "usage: crucible preview <content-dir> [--port 8090] [--image crucible:dev] [--free]")
		return 2
	}
	src, _ := filepath.Abs(dir)
	t, probs := content.Load(src)
	printProblems(probs)
	if t == nil {
		fmt.Fprintln(os.Stderr, "fix training.yaml first: preview needs the training id")
		return 1
	}
	if err := docker(io.Discard, "image", "inspect", *image); err != nil {
		fmt.Fprintf(os.Stderr, "image %s not found; build it from the Crucible repo with: docker build -t %s .\n", *image, *image)
		return 1
	}
	// Caught from here on, so Ctrl-C during startup still reaches the cleanup below instead of killing the process.
	// SIGHUP too: closing the terminal must not strand the stack.
	sigCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	defer stop()
	ctx, cancel := context.WithCancel(sigCtx)
	defer cancel()
	work, err := os.MkdirTemp("", "crucible-preview-*")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer os.RemoveAll(work)
	// work stays 0700 (MkdirTemp): only work/git is opened up (openUp) for the container's uid 10001, and Docker
	// mounts it as root, so no other local user can reach compose.yml or the repos.
	if _, err := snapshot(src, work, *free); err != nil {
		fmt.Fprintln(os.Stderr, "snapshot:", err)
		return 1
	}
	// The preview's configuration: imported into its empty database at start (CRUCIBLE_SEED_DIR=/git/seed).
	if err := writePlatform(filepath.Join(work, "git", "seed"), t.ID); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if err := openUp(filepath.Join(work, "git")); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	compose, token, hook, err := writeCompose(work, *image, *port)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	dc := func(a ...string) error { return docker(os.Stderr, append([]string{"compose", "-f", compose}, a...)...) }
	// Containers, network and volumes are all in this project; down -v removes every one of them.
	defer func() { _ = dc("down", "-v", "--remove-orphans") }()
	fmt.Fprintln(os.Stderr, "starting Crucible preview…")
	if err := dc("up", "-d", "--wait"); err != nil {
		fmt.Fprintln(os.Stderr, "docker compose up failed:", err)
		return 1
	}
	if ctx.Err() != nil {
		return 0
	}
	base := fmt.Sprintf("http://localhost:%d", *port)
	pairing, err := pairingToken(base, token)
	if err != nil {
		fmt.Fprintln(os.Stderr, "preview sign-in failed:", err)
		return 1
	}
	agentDone := make(chan struct{})
	go func() { // the agent runs in this process: local labs start on the author's Docker like a trainee's would
		defer close(agentDone)
		defer func() {
			if r := recover(); r != nil {
				slog.Error("preview agent panicked; stopping the preview", "panic", r)
				cancel() // the main loop sees ctx.Done and runs the cleanup
			}
		}()
		c := &agent.Client{Server: base, Token: pairing, Exec: agent.Compose{Dir: filepath.Join(work, "labs")}, Log: slog.Default()}
		if err := c.Run(ctx); err != nil && ctx.Err() == nil {
			slog.Error("preview agent stopped; local labs will not start", "err", err)
		}
	}()
	fmt.Printf("\nCrucible preview of %q is up.\n   Open: %s/auth/preview?token=%s\n   Edits to tracked files are picked up within a few seconds (git add new files). Ctrl-C stops it.\n\n", t.Title, base, token)
	last, _ := fingerprint(src)
	tick := time.NewTicker(2 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			stop() // a second Ctrl-C now kills the process instead of waiting on a stuck lab teardown
			fmt.Fprintln(os.Stderr, "stopping the preview…")
			<-agentDone // the agent tears its labs down before the stack and the work dir go
			return 0
		case <-tick.C:
		}
		fp, err := fingerprint(src)
		if err != nil || fp == last {
			continue
		}
		last = fp
		_, probs := content.Load(src)
		printProblems(probs)
		if changed, err := snapshot(src, work, *free); err != nil {
			fmt.Fprintln(os.Stderr, "snapshot:", err)
		} else if changed {
			req, _ := http.NewRequest(http.MethodPost, base+"/api/git/hook", nil)
			req.Header.Set("X-Crucible-Secret", hook)
			if resp, err := http.DefaultClient.Do(req); err == nil {
				resp.Body.Close()
			}
			fmt.Fprintln(os.Stderr, "synced", time.Now().Format("15:04:05"))
		}
	}
}

func printProblems(probs []content.Problem) {
	for _, p := range probs {
		fmt.Fprintln(os.Stderr, "✗", p)
	}
	if len(probs) == 0 {
		fmt.Fprintln(os.Stderr, "✓ lint clean")
	}
}

func randHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// trackedFiles lists the files under src that git tracks or has staged, relative to src. Untracked and ignored
// files (a .env, keys, build output) are never previewed; a directory that is not in a git repo is refused.
func trackedFiles(src string) ([]string, error) {
	cmd := exec.Command("git", "-C", src, "ls-files", "-z", "--cached")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("preview snapshots only files git tracks, and %s is not in a git repo (git init && git add .): %s", src, strings.TrimSpace(stderr.String()))
	}
	var files []string
	for f := range strings.SplitSeq(string(out), "\x00") {
		if f != "" {
			files = append(files, filepath.FromSlash(f))
		}
	}
	return files, nil
}

// snapshot copies src's tracked files into work/src, commits any change and pushes it to work/git/content.git.
func snapshot(src, work string, free bool) (bool, error) {
	files, err := trackedFiles(src)
	if err != nil {
		return false, err
	}
	repo, bare := filepath.Join(work, "src"), filepath.Join(work, "git", "content.git")
	if _, err := os.Stat(bare); err != nil {
		if err := os.MkdirAll(repo, 0o755); err != nil {
			return false, err
		}
		if err := gitRun(repo, "init", "-q", "-b", "main"); err != nil {
			return false, err
		}
		if err := gitRun("", "init", "-q", "--bare", "-b", "main", bare); err != nil {
			return false, err
		}
		if err := gitRun(repo, "remote", "add", "origin", bare); err != nil {
			return false, err
		}
	}
	entries, _ := os.ReadDir(repo)
	for _, e := range entries {
		if e.Name() != ".git" {
			_ = os.RemoveAll(filepath.Join(repo, e.Name()))
		}
	}
	for _, rel := range files {
		fi, err := lstatNoLinks(src, rel)
		if errors.Is(err, fs.ErrNotExist) {
			continue // deleted but not yet staged: the preview shows the working tree
		}
		if err != nil {
			return false, err
		}
		if fi == nil || !fi.Mode().IsRegular() { // symlinks (files or directories) are a lint error anyway; submodules are directories
			continue
		}
		b, err := os.ReadFile(filepath.Join(src, rel))
		if err != nil {
			return false, err
		}
		dst := filepath.Join(repo, rel)
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return false, err
		}
		if err := os.WriteFile(dst, b, fi.Mode().Perm()); err != nil {
			return false, err
		}
	}
	if free {
		if err := yamlx.Update(filepath.Join(repo, "training.yaml"), map[string]any{"progression": "free"}); err != nil {
			return false, err
		}
	}
	if err := gitRun(repo, "add", "-A", "-f"); err != nil {
		return false, err
	}
	if gitRun(repo, "diff", "--cached", "--quiet") == nil && gitRun(repo, "rev-parse", "--verify", "-q", "HEAD") == nil {
		return false, nil
	}
	if err := gitRun(repo, "-c", "user.name=crucible preview", "-c", "user.email=preview@crucible.local", "commit", "-q", "--allow-empty", "-m", "preview snapshot"); err != nil {
		return false, err
	}
	if err := gitRun(repo, "push", "-q", "--force", "origin", "HEAD:main"); err != nil {
		return false, err
	}
	return true, openUp(bare) // new objects must stay readable by the container's user
}

// lstatNoLinks lstats src/rel but returns nil if any parent directory of rel is a symlink, so a tracked directory
// later replaced by a link cannot pull files from outside src into the preview.
func lstatNoLinks(src, rel string) (fs.FileInfo, error) {
	dir := src
	parts := strings.Split(rel, string(filepath.Separator))
	for _, p := range parts[:len(parts)-1] {
		dir = filepath.Join(dir, p)
		fi, err := os.Lstat(dir)
		if err != nil {
			return nil, err
		}
		if fi.Mode()&fs.ModeSymlink != 0 {
			return nil, nil
		}
	}
	return os.Lstat(filepath.Join(src, rel))
}

// writeCompose writes work/compose.yml (0600: it holds the preview token, hook secret and DB password) with fresh
// random secrets and returns its path, the sign-in token and the hook secret.
func writeCompose(work, image string, port int) (path, token, hook string, err error) {
	token, hook = randHex(24), randHex(24)
	project, pg := "crucible-preview-"+randHex(3), randHex(16)
	path = filepath.Join(work, "compose.yml")
	err = os.WriteFile(path, []byte(composeFile(project, image, port, token, hook, pg, filepath.Join(work, "git"))), 0o600)
	return
}

func gitRun(dir string, args ...string) error {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, out)
	}
	return nil
}

// writePlatform makes a one-team platform directory (the seed layout) in which the preview author is an admin and
// enrolled in training.
func writePlatform(dir, training string) error {
	files := map[string]string{
		"platform.yaml":           "default_theme: forge\ncost_tiers: {auto_approve_usd: 0, tier1_usd: 1, tier2_usd: 2}\n",
		"admins.yaml":             "admins: [preview@crucible.local]\n",
		"trainings.yaml":          fmt.Sprintf("trainings:\n  %s: {repo: file:///git/content.git, branch: main}\n", training),
		"teams/preview/team.yaml": "name: Preview\nleader: preview@crucible.local\n",
		"teams/preview/programs/" + training + ".yaml": fmt.Sprintf("training: %s\nenrolled: [preview@crucible.local]\n", training),
	}
	for rel, body := range files {
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			return err
		}
	}
	return nil
}

// openUp makes the bare repos readable by the container's user.
func openUp(dir string) error {
	return filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		mode := os.FileMode(0o644)
		if d.IsDir() {
			mode = 0o755
		}
		return os.Chmod(p, mode)
	})
}

// fingerprint changes whenever the tracked file list or any tracked file's size, mode or mtime changes (cheap enough
// to poll every 2 s). Untracked files are not previewed, so they do not count.
func fingerprint(dir string) (string, error) {
	files, err := trackedFiles(dir)
	if err != nil {
		return "", err
	}
	h := sha256.New()
	for _, rel := range files {
		fmt.Fprintf(h, "%s", rel)
		if fi, err := os.Lstat(filepath.Join(dir, rel)); err == nil {
			fmt.Fprintf(h, "|%d|%o|%d", fi.Size(), fi.Mode(), fi.ModTime().UnixNano())
		}
		fmt.Fprintln(h)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// composeFile is the preview stack. Only the API publishes a port, and only on 127.0.0.1; Postgres keeps its data in
// tmpfs. CRUCIBLE_GIT_ALLOW_FILE lets gitsync read the file:// snapshot repo mounted read-only at /git, beside the seed.
func composeFile(project, image string, port int, token, hook, pgPass, gitDir string) string {
	return fmt.Sprintf(`name: %s
services:
  postgres:
    image: postgres:18-alpine
    environment: { POSTGRES_USER: crucible, POSTGRES_PASSWORD: %s, POSTGRES_DB: crucible }
    tmpfs: [/var/lib/postgresql]
    healthcheck: { test: ["CMD-SHELL", "pg_isready -U crucible"], interval: 2s, retries: 30 }
  api:
    image: %q
    environment:
      DATABASE_URL: postgres://crucible:%s@postgres:5432/crucible?sslmode=disable
      CRUCIBLE_SEED_DIR: /git/seed
      CRUCIBLE_PUBLIC_URL: http://localhost:%d
      CRUCIBLE_SYNC_INTERVAL: 30s
      CRUCIBLE_PREVIEW_TOKEN: %q
      CRUCIBLE_GIT_HOOK_SECRET: %q
      CRUCIBLE_GIT_ALLOW_FILE: "1"
      CRUCIBLE_QUIZ_SECRET: crucible-preview
    # The api only reads /git and writes its mirrors under /data (CRUCIBLE_DATA_DIR) and temp files.
    read_only: true
    tmpfs: ["/data:uid=10001,mode=0700", "/tmp"]
    cap_drop: [ALL]
    security_opt: ["no-new-privileges:true"]
    ports: ["127.0.0.1:%d:8080"]
    volumes: [%q]
    depends_on: { postgres: { condition: service_healthy } }
    healthcheck: { test: ["CMD-SHELL", "wget -qO- http://127.0.0.1:8080/healthz"], interval: 2s, retries: 60 }
`, project, pgPass, image, pgPass, port, token, hook, port, gitDir+":/git:ro")
}

// pairingToken signs in through /auth/preview and asks for an agent pairing token the normal way.
func pairingToken(base, token string) (string, error) {
	jar, _ := cookiejar.New(nil)
	c := &http.Client{Jar: jar, Timeout: 15 * time.Second}
	resp, err := c.Get(base + "/auth/preview?token=" + token)
	if err != nil {
		return "", err
	}
	resp.Body.Close()
	req, _ := http.NewRequest(http.MethodPost, base+"/api/agent/tokens", strings.NewReader("{}"))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", base)
	resp, err = c.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		b, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("pairing token: %s %s", resp.Status, b)
	}
	var out struct {
		Token string `json:"token"`
	}
	return out.Token, json.NewDecoder(resp.Body).Decode(&out)
}
