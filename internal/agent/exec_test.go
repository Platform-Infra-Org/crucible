package agent

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"os/exec"
	"strings"
	"testing"
	"time"

	ap "crucible/internal/agentproto"
)

func TestRunLimitedExitCodeAndOutput(t *testing.T) {
	res, err := RunLimited(context.Background(), exec.Command("sh", "-c", "echo hi; exit 3"), 5*time.Second)
	if err != nil || res.ExitCode != 3 || strings.TrimSpace(string(res.Data)) != "hi" {
		t.Fatalf("%+v %v", res, err)
	}
}

func TestRunLimitedTimesOut(t *testing.T) {
	start := time.Now()
	res, err := RunLimited(context.Background(), exec.Command("sh", "-c", "sleep 10"), 200*time.Millisecond)
	if err != nil || !res.TimedOut || res.ExitCode != -1 || time.Since(start) > 3*time.Second {
		t.Fatalf("%+v %v after %v", res, err, time.Since(start))
	}
}

func TestRunLimitedCapsOutput(t *testing.T) {
	res, err := RunLimited(context.Background(), exec.Command("sh", "-c", "yes forge | head -c 500000"), 5*time.Second)
	if err != nil || len(res.Data) != ap.MaxOutput {
		t.Fatalf("len %d err %v", len(res.Data), err)
	}
}

func TestUntarRejectsEscapes(t *testing.T) {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	_ = tw.WriteHeader(&tar.Header{Name: "../evil.sh", Mode: 0o755, Size: 2, Typeflag: tar.TypeReg})
	_, _ = tw.Write([]byte("hi"))
	_ = tw.Close()
	_ = gz.Close()
	if err := Untar(buf.Bytes(), t.TempDir()); err == nil || !strings.Contains(err.Error(), "unsafe path") {
		t.Fatalf("got %v", err)
	}
}
