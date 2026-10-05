package labs

import (
	"bytes"
	"context"
	"errors"
	"io"
	"maps"
	"math"
	"slices"
	"strconv"
	"strings"
	"sync"

	"k8s.io/client-go/tools/remotecommand"
	utilexec "k8s.io/client-go/util/exec"

	ap "crucible/internal/agentproto"
)

var _ Runner = (*ClusterRunner)(nil)

// validService refuses names docker compose would parse as flags. Service names come from lab.yaml (validated
// against the compose file by the loader), never from the browser.
func validService(s string) bool { return s != "" && !strings.HasPrefix(s, "-") }

// RunScript runs a check or setup out of band (spec §8.5): the script travels on stdin and is never stored in the
// lab. Env values (e.g. the trainee's quiz answer) are single argv elements, never shell text.
func (c *ClusterRunner) RunScript(ctx context.Context, inst *Instance, s ScriptSpec) (ScriptResult, error) {
	if !validLabID(inst.ID) || !validService(s.Service) {
		return ScriptResult{}, errors.New("invalid lab id or service")
	}
	// `timeout` inside the pod kills the compose client if our stream dies first.
	// ponytail: like local labs, the process inside the service may linger until the lab is destroyed.
	secs := int(math.Ceil(s.Timeout.Seconds())) + 5
	cmd := []string{"timeout", "-s", "KILL", strconv.Itoa(secs), "docker", "compose", "exec", "-T"}
	for _, k := range slices.Sorted(maps.Keys(s.Env)) {
		cmd = append(cmd, "-e", k+"="+s.Env[k])
	}
	cmd = append(cmd, s.Service, "sh", "-s")
	sctx, cancel := context.WithTimeout(ctx, s.Timeout)
	defer cancel()
	out := &ap.Capped{}
	err := c.execFn()(sctx, labNamespace(inst.ID), labPod, cmd,
		remotecommand.StreamOptions{Stdin: bytes.NewReader(s.Script), Stdout: out, Stderr: out})
	var exit utilexec.ExitError
	switch {
	case ctx.Err() != nil:
		return ScriptResult{}, ctx.Err() // the caller gave up: not the script's fault
	case sctx.Err() != nil:
		return ScriptResult{ExitCode: -1, TimedOut: true, Output: string(out.Bytes()) + "\n[crucible] script timed out"}, nil
	case errors.As(err, &exit):
		return ScriptResult{ExitCode: exit.ExitStatus(), Output: string(out.Bytes())}, nil
	case err != nil:
		return ScriptResult{}, err
	}
	return ScriptResult{Output: string(out.Bytes())}, nil
}

// OpenPTY opens a login shell in a compose service over a TTY exec. The stream runs until Close or until the
// exec ends; a dead stream surfaces as a read error, which closes the trainee's terminal websocket.
func (c *ClusterRunner) OpenPTY(ctx context.Context, inst *Instance, service string, cols, rows int) (PTY, error) {
	if !validLabID(inst.ID) || !validService(service) {
		return nil, errors.New("invalid lab id or service")
	}
	ctx, cancel := context.WithCancel(ctx)
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	p := &execPTY{in: inW, out: outR, sizes: make(chan remotecommand.TerminalSize, 1), done: make(chan struct{}), cancel: cancel}
	p.sizes <- remotecommand.TerminalSize{Width: uint16(cols), Height: uint16(rows)}
	cmd := []string{"docker", "compose", "exec", service, "sh", "-c",
		"if command -v bash >/dev/null 2>&1; then exec bash -l; else exec sh -l; fi"}
	go func() {
		err := c.execFn()(ctx, labNamespace(inst.ID), labPod, cmd,
			remotecommand.StreamOptions{Stdin: inR, Stdout: outW, Tty: true, TerminalSizeQueue: p})
		if err == nil {
			err = io.EOF
		}
		outW.CloseWithError(err)
		inR.CloseWithError(err)
	}()
	return p, nil
}

type execPTY struct {
	in     *io.PipeWriter
	out    *io.PipeReader
	sizes  chan remotecommand.TerminalSize // holds at most the latest size the stream has not picked up
	done   chan struct{}
	once   sync.Once
	cancel context.CancelFunc
}

func (p *execPTY) Read(b []byte) (int, error)  { return p.out.Read(b) }
func (p *execPTY) Write(b []byte) (int, error) { return p.in.Write(b) }

// Resize keeps only the newest size; it never blocks (one caller: the websocket read loop).
func (p *execPTY) Resize(cols, rows int) error {
	select {
	case <-p.sizes:
	default:
	}
	select {
	case p.sizes <- remotecommand.TerminalSize{Width: uint16(cols), Height: uint16(rows)}:
	default:
	}
	return nil
}

// Next implements remotecommand.TerminalSizeQueue; nil ends the size stream.
func (p *execPTY) Next() *remotecommand.TerminalSize {
	select {
	case s := <-p.sizes:
		return &s
	case <-p.done:
		return nil
	}
}

func (p *execPTY) Close() error {
	p.once.Do(func() {
		close(p.done)
		p.cancel()
		_ = p.in.Close()
		_ = p.out.Close()
	})
	return nil
}
