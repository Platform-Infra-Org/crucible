package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type fakeKind struct {
	have            []string
	created, delete []string
	image, path     string
}

func (f *fakeKind) List() ([]string, error) { return f.have, nil }
func (f *fakeKind) Create(name, image, kubeconfig string) error {
	f.created = append(f.created, name)
	f.image, f.path = image, kubeconfig
	return os.WriteFile(kubeconfig, []byte(strings.ReplaceAll(twoContexts, "kind-dev", "kind-"+name)), 0o600)
}
func (f *fakeKind) Delete(name, kubeconfig string) error {
	f.delete = append(f.delete, name)
	return nil
}

func TestClusterUpCreatesOnceIntoCrucibleKubeconfig(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	f := &fakeKind{}
	var out strings.Builder
	if err := ClusterUp(f, "crucible", &out); err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(home, ".crucible", "kubeconfig")
	if len(f.created) != 1 || f.image != KindNodeImage || f.path != want {
		t.Fatalf("%+v", f)
	}
	if st, err := os.Stat(want); err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("kubeconfig %v %v", st, err)
	}
	if !strings.Contains(out.String(), "kind-crucible") {
		t.Fatal(out.String())
	}
	if _, err := os.Stat(filepath.Join(home, ".kube")); err == nil {
		t.Fatal("touched ~/.kube")
	}
	f.have = []string{"crucible"}
	out.Reset()
	if err := ClusterUp(f, "crucible", &out); err != nil || len(f.created) != 1 || !strings.Contains(out.String(), "already") {
		t.Fatalf("second run: %v %+v %s", err, f.created, out.String())
	}
}

func TestClusterDownDeletesAndCleansKubeconfig(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	f := &fakeKind{}
	_ = ClusterUp(f, "crucible", &strings.Builder{})
	// add a second cluster's entries that must survive
	path := f.path
	f.have = []string{"crucible"}
	if err := ClusterDown(f, "crucible", &strings.Builder{}); err != nil {
		t.Fatal(err)
	}
	if len(f.delete) != 1 || f.delete[0] != "crucible" {
		t.Fatalf("%+v", f.delete)
	}
	// twoContexts held kind-crucible + prod-eks: only the first goes
	raw, err := os.ReadFile(path)
	if err != nil || strings.Contains(string(raw), "kind-crucible") || !strings.Contains(string(raw), "prod-eks") {
		t.Fatalf("%v %s", err, raw)
	}
}

func TestClusterDownRemovesEmptyKubeconfig(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	p := filepath.Join(home, ".crucible", "kubeconfig")
	_ = os.MkdirAll(filepath.Dir(p), 0o700)
	only := "apiVersion: v1\nkind: Config\nclusters:\n- name: kind-crucible\n  cluster: {server: 'https://127.0.0.1:1'}\ncontexts:\n- name: kind-crucible\n  context: {cluster: kind-crucible, user: kind-crucible}\nusers:\n- name: kind-crucible\n  user: {token: x}\n"
	_ = os.WriteFile(p, []byte(only), 0o600)
	f := &fakeKind{have: []string{"crucible"}}
	if err := ClusterDown(f, "crucible", &strings.Builder{}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(p); err == nil {
		t.Fatal("empty kubeconfig left behind")
	}
}
