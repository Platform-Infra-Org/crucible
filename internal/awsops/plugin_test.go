package awsops

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// stubPluginGz is what recorder.ops() carries: a plugin-shaped blob that costs
// nothing to extract, so no test depends on whether this tree has run
// scripts/plugin-blobs.sh and none writes out the real 13 MB binary.
var stubPluginGz = sync.OnceValue(func() []byte {
	var b bytes.Buffer
	w := gzip.NewWriter(&b)
	_, _ = w.Write([]byte("#!/bin/sh\nexit 0\n"))
	_ = w.Close()
	return b.Bytes()
})

// gzBytes is a stand-in for the embedded plugin: tests must not depend on
// whether scripts/plugin-blobs.sh has run in this tree, so every test that
// needs a plugin carries its own.
func gzBytes(t *testing.T, content string) []byte {
	t.Helper()
	var b bytes.Buffer
	w := gzip.NewWriter(&b)
	if _, err := w.Write([]byte(content)); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// The point of embedding the plugin: connect needs nothing installed. No AWS
// CLI, no session-manager-plugin on PATH — the binary carries both halves, so
// the tunnel opens on a machine where PATH holds neither.
func TestConnectRunsTheEmbeddedPluginAndNotTheAWSCLI(t *testing.T) {
	r := &recorder{output: fakeNode("i-0abc\thttp://localhost:18080")}
	home := t.TempDir()
	o := r.ops(home)
	o.Plugin = gzBytes(t, "#!/bin/sh\nexit 0\n")
	o.LookPath = func(file string) (string, error) {
		return "", errors.New(file + ": executable file not found in $PATH")
	}
	var ran string
	o.Exec = func(_ context.Context, _ string, name string, args ...string) error {
		ran = name + " " + strings.Join(args, " ")
		return nil
	}
	if err := o.Connect(context.Background(), ConnectOpts{Region: "il-central-1"}); err != nil {
		t.Fatalf("connect must not need anything on PATH: %v", err)
	}
	if want := filepath.Join(home, "bin"); !strings.HasPrefix(ran, want) {
		t.Fatalf("connect ran %q, want the plugin extracted under %s", ran, want)
	}
	// The six arguments the AWS CLI passes the plugin, in its order: the
	// StartSession response, the region, the operation, the profile, the
	// request parameters, and the SSM endpoint.
	args := strings.Split(ran, " ")
	if len(args) != 7 {
		t.Fatalf("the plugin takes six arguments, got %d: %s", len(args)-1, ran)
	}
	var resp struct{ SessionId, TokenValue, StreamUrl string }
	if err := json.Unmarshal([]byte(args[1]), &resp); err != nil {
		t.Fatalf("argument 1 must be the StartSession response as JSON: %v (%s)", err, args[1])
	}
	if resp.SessionId == "" || resp.TokenValue == "" || !strings.HasPrefix(resp.StreamUrl, "wss://") {
		t.Errorf("the response must carry the session, its token and the stream URL: %+v", resp)
	}
	if args[2] != "il-central-1" || args[3] != "StartSession" {
		t.Errorf("arguments 2 and 3 are the region and the operation: %q %q", args[2], args[3])
	}
	var params struct {
		Target, DocumentName string
		Parameters           map[string][]string
	}
	if err := json.Unmarshal([]byte(args[5]), &params); err != nil {
		t.Fatalf("argument 5 must be the StartSession parameters as JSON: %v (%s)", err, args[5])
	}
	if params.Target != "i-0abc" || params.DocumentName != "AWS-StartPortForwardingSession" {
		t.Errorf("the parameters must name the node and the port-forward document: %+v", params)
	}
	if got := params.Parameters["localPortNumber"]; len(got) != 1 || got[0] != "18080" {
		t.Errorf("localPortNumber must be the node's own port: %v", params.Parameters)
	}
	if args[6] != "https://ssm.il-central-1.amazonaws.com" {
		t.Errorf("argument 6 is the SSM endpoint for the region: %q", args[6])
	}
}

// Extraction happens once: the plugin is ~13 MB, so writing it on every connect
// would be a visible pause, and the licence AWS ships with it must land beside
// it because Apache-2.0 asks that the notice travel with the binary.
func TestTheExtractedPluginIsCachedAndCarriesItsLicence(t *testing.T) {
	r := &recorder{output: fakeNode("i-0abc\thttp://localhost:18080")}
	home := t.TempDir()
	o := r.ops(home)
	o.Plugin = gzBytes(t, "#!/bin/sh\nexit 0\n")

	if err := o.Connect(context.Background(), ConnectOpts{Region: "il-central-1"}); err != nil {
		t.Fatal(err)
	}
	path, err := o.sessionPlugin()
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("the plugin was not extracted: %v", err)
	}
	if info.Mode().Perm() != 0o700 {
		t.Errorf("mode %v, want 0700: it is a binary this user runs", info.Mode().Perm())
	}
	for _, f := range []string{"session-manager-plugin.LICENSE", "session-manager-plugin.NOTICE"} {
		if _, err := os.Stat(filepath.Join(filepath.Dir(path), f)); err != nil {
			t.Errorf("Apache-2.0 asks that %s travel with the binary: %v", f, err)
		}
	}

	// A second connect reuses the file rather than writing it again.
	before := info.ModTime()
	if err := o.Connect(context.Background(), ConnectOpts{Region: "il-central-1"}); err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !after.ModTime().Equal(before) {
		t.Error("the second connect rewrote the plugin instead of reusing the cached copy")
	}
}

// A development build carries no plugin (the blobs are placeholders until
// scripts/plugin-blobs.sh runs), so it must still work the old way: a plugin on
// PATH, and a clear message when there is none.
func TestWithoutAnEmbeddedPluginConnectUsesThePath(t *testing.T) {
	r := &recorder{output: fakeNode("i-0abc\thttp://localhost:18080")}
	o := r.ops(t.TempDir()) // Plugin is nil, and the recorder's LookPath finds one
	o.Plugin = []byte{}
	var ran string
	o.Exec = func(_ context.Context, _ string, name string, _ ...string) error { ran = name; return nil }
	if err := o.Connect(context.Background(), ConnectOpts{Region: "il-central-1"}); err != nil {
		t.Fatalf("a dev build must fall back to a plugin on PATH: %v", err)
	}
	if ran != "/usr/local/bin/session-manager-plugin" {
		t.Errorf("the plugin on PATH was not run, got %q", ran)
	}

	r2 := &recorder{output: fakeNode("i-0abc\thttp://localhost:18080"), noPlugin: true}
	o2 := r2.ops(t.TempDir())
	o2.Plugin = []byte{}
	err := o2.Connect(context.Background(), ConnectOpts{Region: "il-central-1"})
	if err == nil || !strings.Contains(err.Error(), "session-manager-plugin") {
		t.Fatalf("no plugin anywhere must say so plainly: %v", err)
	}
}

// A corrupt blob must fail as a corrupt blob, not as "exec format error" from a
// half-written file that then sits in the cache for ever.
func TestACorruptEmbeddedPluginIsReported(t *testing.T) {
	r := &recorder{output: fakeNode("i-0abc\thttp://localhost:18080")}
	home := t.TempDir()
	o := r.ops(home)
	o.Plugin = []byte("this is not gzip")
	err := o.Connect(context.Background(), ConnectOpts{Region: "il-central-1"})
	if err == nil || !strings.Contains(err.Error(), "session-manager-plugin") {
		t.Fatalf("a corrupt embedded plugin must be named as such: %v", err)
	}
	if entries, _ := os.ReadDir(filepath.Join(home, "bin")); len(entries) > 0 {
		t.Errorf("a failed extraction must leave nothing behind: %v", entries)
	}
}
