package agent

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/creack/pty"

	ap "crucible/internal/agentproto"
)

type Session interface {
	io.ReadWriteCloser
	Resize(cols, rows int) error
}

type Executor interface {
	Provision(ctx context.Context, labID string, bundle []byte, compose string) error
	Destroy(ctx context.Context, labID string) error
	RunScript(ctx context.Context, labID, service string, script []byte, env map[string]string, timeout time.Duration) (ap.Msg, error)
	StartPTY(labID, service string, cols, rows int) (Session, error)
	DestroyAll(ctx context.Context)
	Labs() []string // ids of labs currently on this machine
}

// Compose runs each lab as a docker compose project named crucible-<labID> under Dir/<labID>.
type Compose struct{ Dir string }

const composeMarker = ".crucible-compose"

// labIDPattern matches the server's lab ids (12 hex chars); anything else could name a path.
var labIDPattern = regexp.MustCompile(`^[0-9a-f]{12}$`)

func validID(id string) error {
	if !labIDPattern.MatchString(id) {
		return errors.New("invalid lab id")
	}
	return nil
}

func (c Compose) labDir(labID string) string { return filepath.Join(c.Dir, labID) }

func (c Compose) args(labID string, rest ...string) []string {
	name, _ := os.ReadFile(filepath.Join(c.labDir(labID), composeMarker))
	file := filepath.Join(c.labDir(labID), strings.TrimSpace(string(name)))
	return append([]string{"compose", "-p", "crucible-" + labID, "-f", file}, rest...)
}

func (c Compose) Provision(ctx context.Context, labID string, bundle []byte, compose string) error {
	if validID(labID) != nil || !filepath.IsLocal(compose) {
		return errors.New("invalid lab id or compose file name")
	}
	dir := c.labDir(labID)
	_ = os.RemoveAll(dir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	if err := Untar(bundle, dir); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, composeMarker), []byte(compose), 0o644); err != nil {
		return err
	}
	out, err := docker(ctx, c.args(labID, "up", "-d", "--wait")...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("docker compose up: %w: %s", err, tail(out))
	}
	return nil
}

func (c Compose) Destroy(ctx context.Context, labID string) error {
	if err := validID(labID); err != nil {
		return err
	}
	dir := c.labDir(labID)
	if _, err := os.Stat(dir); errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	out, err := docker(ctx, c.args(labID, "down", "-v", "--remove-orphans")...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("docker compose down: %w: %s", err, tail(out))
	}
	return os.RemoveAll(dir)
}

func (c Compose) DestroyAll(ctx context.Context) {
	for _, id := range c.Labs() {
		_ = c.Destroy(ctx, id)
	}
}

func (c Compose) Labs() []string {
	entries, _ := os.ReadDir(c.Dir)
	ids := []string{}
	for _, e := range entries {
		if e.IsDir() && validID(e.Name()) == nil {
			ids = append(ids, e.Name())
		}
	}
	return ids
}

func (c Compose) RunScript(ctx context.Context, labID, service string, script []byte, env map[string]string, timeout time.Duration) (ap.Msg, error) {
	if err := validID(labID); err != nil {
		return ap.Msg{}, err
	}
	args := c.args(labID, "exec", "-T")
	for k, v := range env {
		args = append(args, "-e", k+"="+v)
	}
	args = append(args, service, "sh", "-s")
	cmd := docker(context.Background(), args...)
	cmd.Stdin = bytes.NewReader(script)
	return RunLimited(ctx, cmd, timeout)
}

func (c Compose) StartPTY(labID, service string, cols, rows int) (Session, error) {
	if err := validID(labID); err != nil {
		return nil, err
	}
	cmd := docker(context.Background(), c.args(labID, "exec", service, "sh", "-c",
		"if command -v bash >/dev/null 2>&1; then exec bash -l; else exec sh -l; fi")...)
	f, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: uint16(cols), Rows: uint16(rows)})
	if err != nil {
		return nil, err
	}
	return &ptySession{f: f, cmd: cmd}, nil
}

type ptySession struct {
	f   *os.File
	cmd *exec.Cmd
}

func (p *ptySession) Read(b []byte) (int, error)  { return p.f.Read(b) }
func (p *ptySession) Write(b []byte) (int, error) { return p.f.Write(b) }
func (p *ptySession) Resize(cols, rows int) error {
	return pty.Setsize(p.f, &pty.Winsize{Cols: uint16(cols), Rows: uint16(rows)})
}
func (p *ptySession) Close() error {
	_ = p.f.Close()
	_ = p.cmd.Process.Kill()
	_ = p.cmd.Wait()
	return nil
}

func tail(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > 2000 {
		s = "…" + s[len(s)-2000:]
	}
	return s
}

// composeEnv is the environment docker compose runs with: just enough to find and reach Docker. Compose files
// interpolate ${VAR}; with the agent's own environment a lab could read CRUCIBLE_TOKEN or cloud credentials.
func composeEnv(environ []string) []string {
	var out []string
	for _, kv := range environ {
		k, _, _ := strings.Cut(kv, "=")
		if k == "PATH" || k == "HOME" || k == "USER" || k == "TMPDIR" || k == "XDG_RUNTIME_DIR" || strings.HasPrefix(k, "DOCKER_") {
			out = append(out, kv)
		}
	}
	return out
}

func docker(ctx context.Context, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "docker", args...)
	cmd.Env = composeEnv(os.Environ())
	return cmd
}
