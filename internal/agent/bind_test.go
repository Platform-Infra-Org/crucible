package agent

import (
	"context"
	"errors"
	"testing"
)

func TestBindAddrIsOnlyReachableFromLocalContainers(t *testing.T) {
	docker := func(out map[string]string) func(context.Context, ...string) ([]byte, error) {
		return func(_ context.Context, args ...string) ([]byte, error) {
			if v, ok := out[args[0]]; ok {
				return []byte(v), nil
			}
			return nil, errors.New("no docker")
		}
	}
	for name, c := range map[string]struct {
		goos string
		out  map[string]string
		want string
	}{
		"macOS":          {"darwin", nil, "127.0.0.1"},
		"Windows":        {"windows", nil, "127.0.0.1"},
		"Docker Desktop": {"linux", map[string]string{"info": "Docker Desktop\n"}, "127.0.0.1"},
		"Linux bridge":   {"linux", map[string]string{"info": "Ubuntu 24.04\n", "network": "172.17.0.1\n"}, "172.17.0.1"},
	} {
		got, err := BindAddr(context.Background(), c.goos, docker(c.out))
		if err != nil || got != c.want {
			t.Errorf("%s: %q %v", name, got, err)
		}
	}
	if _, err := BindAddr(context.Background(), "linux", docker(map[string]string{"info": "x", "network": "0.0.0.0"})); err == nil {
		t.Error("0.0.0.0 must be refused")
	}
}
