package main

import (
	"strings"
	"testing"
)

func TestCheckServer(t *testing.T) {
	for s, ok := range map[string]bool{
		"https://crucible.example.com": true, "http://localhost:8080": true, "http://127.0.0.1:8080": true,
		"http://[::1]:8080": true, "http://crucible.example.com": false, "http://10.0.0.5:8080": false,
		"ftp://localhost": false, "localhost:8080": false, "http://localhost.evil.com": false,
	} {
		if got := checkServer(s) == nil; got != ok {
			t.Errorf("%s: allowed=%v, want %v", s, got, ok)
		}
	}
}

type fakeKind struct{ calls, have []string }

func (f *fakeKind) List() ([]string, error) { return f.have, nil }
func (f *fakeKind) Create(name, image, kc string) error {
	f.calls = append(f.calls, "create "+name)
	f.have = append(f.have, name)
	return nil
}
func (f *fakeKind) Delete(name, kc string) error {
	f.calls = append(f.calls, "delete "+name)
	return nil
}

func TestClusterCmd(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	f := &fakeKind{}
	var out, errw strings.Builder
	if c := clusterCmd([]string{"up", "--name", "x"}, f, &out, &errw); c != 0 {
		t.Fatal(c, errw.String())
	}
	if c := clusterCmd([]string{"down"}, f, &out, &errw); c != 0 { // no cluster called crucible: nothing to delete
		t.Fatal(c, errw.String())
	}
	if c := clusterCmd([]string{"down", "--name", "x"}, f, &out, &errw); c != 0 {
		t.Fatal(c, errw.String())
	}
	if strings.Join(f.calls, ",") != "create x,delete x" {
		t.Fatal(f.calls)
	}
	if clusterCmd([]string{"sideways"}, f, &out, &errw) != 2 {
		t.Fatal("bad subcommand accepted")
	}
}
