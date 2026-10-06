package blob

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"crucible/internal/apperr"
)

func roundTrip(t *testing.T, s Store) {
	t.Helper()
	ctx := context.Background()
	body := []byte("heat 1200C\nquench\n")
	if err := s.Put(ctx, "uploads/files/0a1b2c", bytes.NewReader(body), int64(len(body))); err != nil {
		t.Fatal(err)
	}
	rc, err := s.Get(ctx, "uploads/files/0a1b2c")
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(rc)
	rc.Close()
	if !bytes.Equal(got, body) {
		t.Fatalf("got %q", got)
	}
	if _, err := s.Get(ctx, "uploads/files/missing"); !errors.Is(err, apperr.NotFound) {
		t.Fatalf("missing key: %v", err)
	}
	for _, bad := range []string{"../etc/passwd", "uploads/../x", "uploads//x", "/uploads/x", "snapshots/x", "uploads/x.html", "uploads/X"} {
		if err := s.Put(ctx, bad, strings.NewReader("x"), 1); err == nil {
			t.Errorf("key %q accepted", bad)
		}
	}
}

func TestDisk(t *testing.T) { roundTrip(t, Disk{Dir: t.TempDir()}) }

// fakeS3 is just enough of the S3 REST API for PutObject and GetObject (path-style).
type fakeS3 struct {
	mu      sync.Mutex
	objects map[string][]byte
	puts    []*http.Request
}

func (f *fakeS3) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch r.Method {
	case http.MethodPut:
		b, _ := io.ReadAll(r.Body)
		f.objects[r.URL.Path] = b
		f.puts = append(f.puts, r)
	case http.MethodGet:
		b, ok := f.objects[r.URL.Path]
		if !ok {
			w.Header().Set("Content-Type", "application/xml")
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, `<?xml version="1.0" encoding="UTF-8"?><Error><Code>NoSuchKey</Code><Message>gone</Message></Error>`)
			return
		}
		_, _ = w.Write(b)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func TestS3AgainstFake(t *testing.T) {
	// Never reach real AWS: static fake credentials, no IMDS, no shared config.
	t.Setenv("AWS_ACCESS_KEY_ID", "AKIAFAKE")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "fake")
	t.Setenv("AWS_EC2_METADATA_DISABLED", "true")
	t.Setenv("AWS_CONFIG_FILE", "/nonexistent")
	t.Setenv("AWS_SHARED_CREDENTIALS_FILE", "/nonexistent")
	t.Setenv("AWS_PROFILE", "")
	t.Setenv("AWS_SHARED_CREDENTIALS_FILE", "/nonexistent")
	fake := &fakeS3{objects: map[string][]byte{}}
	srv := httptest.NewServer(fake)
	defer srv.Close()
	s, err := NewS3(context.Background(), "crucible-data", "eu-west-1", srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	roundTrip(t, s)
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if len(fake.puts) != 1 {
		t.Fatalf("puts = %d", len(fake.puts))
	}
	p := fake.puts[0]
	if p.URL.Path != "/crucible-data/uploads/files/0a1b2c" {
		t.Fatalf("path %q (want path-style bucket/key)", p.URL.Path)
	}
	if p.Header.Get("X-Amz-Server-Side-Encryption") != "AES256" || !strings.HasPrefix(p.Header.Get("Authorization"), "AWS4-HMAC-SHA256") {
		t.Fatalf("headers: %v", p.Header)
	}
}

func TestDiskHardening(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	d := Disk{Dir: filepath.Join(root, "store")}
	for _, bad := range []string{"uploads/a\\b", "uploads/a\x00b", "uploads/..", "uploads/a/../../b", "/etc/passwd", "uploads/" + strings.Repeat("a", 300)} {
		if err := d.Put(ctx, bad, strings.NewReader("x"), 1); err == nil {
			t.Errorf("key %q accepted", bad)
		}
		if _, err := d.Get(ctx, bad); err == nil || errors.Is(err, apperr.NotFound) {
			t.Errorf("get %q: %v", bad, err)
		}
	}
	// size bounds and declared-size mismatch
	if d.Put(ctx, "uploads/a", strings.NewReader("x"), MaxSize+1) == nil || d.Put(ctx, "uploads/a", strings.NewReader("x"), -1) == nil {
		t.Error("bad size accepted")
	}
	if d.Put(ctx, "uploads/a", strings.NewReader("toolong"), 2) == nil || d.Put(ctx, "uploads/a", strings.NewReader("x"), 5) == nil {
		t.Error("size mismatch accepted")
	}
	// symlinked directory and symlinked file are refused
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret"), []byte("s"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(d.Dir, "uploads"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(d.Dir, "uploads", "dir")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "secret"), filepath.Join(d.Dir, "uploads", "file")); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"uploads/dir/secret", "uploads/file"} {
		if _, err := d.Get(ctx, k); err == nil {
			t.Errorf("get through symlink %q allowed", k)
		}
		if err := d.Put(ctx, k, strings.NewReader("x"), 1); err == nil {
			t.Errorf("put through symlink %q allowed", k)
		}
	}
	if b, _ := os.ReadFile(filepath.Join(outside, "secret")); string(b) != "s" {
		t.Fatalf("outside file modified: %q", b)
	}
}
