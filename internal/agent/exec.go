package agent

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	ap "crucible/internal/agentproto"
)

// RunLimited runs cmd with a timeout and keeps at most MaxOutput bytes of combined output.
// ponytail: killing `docker compose exec` stops the client; the in-container process may linger until the lab is destroyed.
func RunLimited(ctx context.Context, cmd *exec.Cmd, timeout time.Duration) (ap.Msg, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	out := &ap.Capped{}
	cmd.Stdout, cmd.Stderr = out, out
	cmd.WaitDelay = 2 * time.Second
	if err := cmd.Start(); err != nil {
		return ap.Msg{}, err
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		var ee *exec.ExitError
		switch {
		case errors.As(err, &ee):
			return ap.Msg{ExitCode: ee.ExitCode(), Data: out.Bytes()}, nil
		case err != nil:
			return ap.Msg{}, err
		}
		return ap.Msg{Data: out.Bytes()}, nil
	case <-ctx.Done():
		_ = cmd.Process.Kill()
		<-done
		return ap.Msg{ExitCode: -1, TimedOut: true, Data: append(out.Bytes(), "\n[crucible] script timed out"...)}, nil
	}
}

// Untar unpacks a gzip'd tar into dir, refusing any entry that would land outside it.
func Untar(bundle []byte, dir string) error {
	gz, err := gzip.NewReader(bytes.NewReader(bundle))
	if err != nil {
		return err
	}
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		name := filepath.FromSlash(strings.TrimSuffix(h.Name, "/"))
		if !filepath.IsLocal(name) {
			return fmt.Errorf("unsafe path %q in lab bundle", h.Name)
		}
		p := filepath.Join(dir, name)
		switch h.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(p, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
				return err
			}
			f, err := os.OpenFile(p, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, fs.FileMode(h.Mode)&0o777)
			if err != nil {
				return err
			}
			_, err = io.Copy(f, io.LimitReader(tr, 64<<20))
			f.Close()
			if err != nil {
				return err
			}
		}
	}
}
