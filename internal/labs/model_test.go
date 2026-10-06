package labs

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/rand"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"crucible/internal/apperr"
	"crucible/internal/config"
	"crucible/internal/content"
	"crucible/internal/gitsync"
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
	for _, f := range []string{"compose.yaml", "conf/nginx.conf", "terraform/main.tf", "lab.yaml", "checks/1.sh", "setup/1.sh", "tasks/1.md", "hints/1.md", "quiz.yaml", "module.yaml"} {
		_ = os.MkdirAll(filepath.Join(dir, filepath.Dir(f)), 0o755)
		_ = os.WriteFile(filepath.Join(dir, f), []byte("x"), 0o644)
	}
	if got := bundled(t, dir, "local"); !slices.Equal(got, []string{"compose.yaml", "conf/nginx.conf", "terraform/main.tf"}) {
		t.Fatalf("local bundle contains %v", got)
	}
	// an aws lab's terraform/ is its module, run by the runner, not the trainee
	if got := bundled(t, dir, "aws"); !slices.Equal(got, []string{"compose.yaml", "conf/nginx.conf"}) {
		t.Fatalf("aws bundle contains %v", got)
	}
}

func bundled(t *testing.T, dir, runtime string) []string {
	t.Helper()
	b, err := Bundle(dir, runtime)
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
	return files
}

func TestBundleSizeLimit(t *testing.T) {
	dir := t.TempDir()
	noise := make([]byte, 64<<10)
	_, _ = rand.Read(noise) // incompressible
	_ = os.WriteFile(filepath.Join(dir, "big.bin"), noise, 0o644)
	defer func(old int) { maxBundleBytes = old }(maxBundleBytes)
	maxBundleBytes = 16 << 10
	if _, err := Bundle(dir, "local"); err == nil || !strings.Contains(err.Error(), "lab bundle exceeds") {
		t.Fatalf("want size error, got %v", err)
	}
	maxBundleBytes = 1 << 20
	if _, err := Bundle(dir, "local"); err != nil {
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

func TestPlatformRatePricesClusterLabs(t *testing.T) {
	ctx := context.Background()
	lab := &content.Lab{ID: "cluster-heat"}
	st := &gitsync.State{Platform: &config.Platform{}}
	r := PlatformRate{State: func() *gitsync.State { return st }, Override: FixedRates{"dev-lab": 9}}
	if _, err := r.HourlyUSD(ctx, lab); !errors.Is(err, apperr.Unavailable) {
		t.Fatalf("no rate configured must be Unavailable, never $0: %v", err)
	}
	if _, err := (PlatformRate{}).HourlyUSD(ctx, lab); !errors.Is(err, apperr.Unavailable) {
		t.Fatalf("nil state: %v", err)
	}
	half := 0.5
	st.Platform.Settings.ClusterUSDPerHour = &half
	if v, err := r.HourlyUSD(ctx, lab); err != nil || v != 0.5 {
		t.Fatalf("rate %v %v", v, err)
	}
	if v, _ := r.HourlyUSD(ctx, &content.Lab{ID: "dev-lab"}); v != 9 {
		t.Fatalf("dev override wins: %v", v)
	}
	zero := 0.0
	st.Platform.Settings.ClusterUSDPerHour = &zero
	if v, err := r.HourlyUSD(ctx, lab); err != nil || v != 0 {
		t.Fatalf("0 means free on the node: %v %v", v, err)
	}
}
