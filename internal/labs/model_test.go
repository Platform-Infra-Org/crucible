package labs

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/rand"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"crucible/internal/config"
	"crucible/internal/content"
	"crucible/internal/yamlx"
)

func TestEffectiveEndPicksEarliest(t *testing.T) {
	base := time.Date(2026, 10, 5, 9, 0, 0, 0, time.UTC)
	got := EffectiveEnd(Limit{base.Add(2 * time.Hour), "ttl"}, Limit{}, Limit{base.Add(time.Hour), "schedule"})
	if got.Reason != "schedule" || !got.At.Equal(base.Add(time.Hour)) {
		t.Fatalf("%+v", got)
	}
}

func TestResolveTimingPrecedence(t *testing.T) {
	lab := &content.Lab{IdleWarning: yamlx.Duration(5 * time.Minute)}
	d := config.LabDefaults{TTL: yamlx.Duration(4 * time.Hour), MaxExtension: yamlx.Duration(30 * time.Minute)}
	tm := ResolveTiming(lab, d)
	if tm.TTL != 4*time.Hour || tm.IdleTimeout != 30*time.Minute || tm.MaxExtension != 30*time.Minute {
		t.Fatalf("program defaults: %+v", tm)
	}
	lab.TTL, lab.IdleTimeout = yamlx.Duration(time.Hour), yamlx.Duration(4*time.Minute)
	tm = ResolveTiming(lab, d)
	if tm.TTL != time.Hour || tm.IdleTimeout != 4*time.Minute || tm.IdleWarning != 2*time.Minute {
		t.Fatalf("lab overrides + warning clamp: %+v", tm)
	}
}

func TestBundleExcludesSecrets(t *testing.T) {
	dir := t.TempDir()
	for _, f := range []string{"compose.yaml", "conf/nginx.conf", "lab.yaml", "checks/1.sh", "setup/1.sh", "tasks/1.md", "hints/1.md", "quiz.yaml", "module.yaml"} {
		_ = os.MkdirAll(filepath.Join(dir, filepath.Dir(f)), 0o755)
		_ = os.WriteFile(filepath.Join(dir, f), []byte("x"), 0o644)
	}
	b, err := Bundle(dir)
	if err != nil {
		t.Fatal(err)
	}
	gz, _ := gzip.NewReader(bytes.NewReader(b))
	tr := tar.NewReader(gz)
	var files []string
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if h.Typeflag == tar.TypeReg {
			files = append(files, h.Name)
		}
	}
	sort.Strings(files)
	if len(files) != 2 || files[0] != "compose.yaml" || files[1] != "conf/nginx.conf" {
		t.Fatalf("bundle contains %v", files)
	}
}

func TestBundleSizeLimit(t *testing.T) {
	dir := t.TempDir()
	noise := make([]byte, 64<<10)
	_, _ = rand.Read(noise) // incompressible
	_ = os.WriteFile(filepath.Join(dir, "big.bin"), noise, 0o644)
	defer func(old int) { maxBundleBytes = old }(maxBundleBytes)
	maxBundleBytes = 16 << 10
	if _, err := Bundle(dir); err == nil || !strings.Contains(err.Error(), "lab bundle exceeds") {
		t.Fatalf("want size error, got %v", err)
	}
	maxBundleBytes = 1 << 20
	if _, err := Bundle(dir); err != nil {
		t.Fatal(err)
	}
}

func TestParseRates(t *testing.T) {
	r, err := ParseRates(" paid-heat=0.5, other=2 ")
	if err != nil || r["paid-heat"] != 0.5 || r["other"] != 2 {
		t.Fatalf("%v %v", r, err)
	}
	if r, err := ParseRates(""); err != nil || len(r) != 0 {
		t.Fatalf("empty: %v %v", r, err)
	}
	for _, bad := range []string{"x", "x=-1", "x=abc"} {
		if _, err := ParseRates(bad); err == nil {
			t.Errorf("%q must be rejected", bad)
		}
	}
}
