package agent

import (
	"context"
	"fmt"
	"net"
	"strings"
)

// BindAddr is where per-lab proxies listen for containers on this machine: loopback where Docker
// runs in a VM that forwards host.docker.internal to it (macOS, Windows, Docker Desktop for Linux), else the Docker
// bridge's gateway (what host.docker.internal:host-gateway points to). Never 0.0.0.0. The bridge gateway can be reachable beyond local containers on Linux, so the per-lab token is the real gate.
func BindAddr(ctx context.Context, goos string, docker func(ctx context.Context, args ...string) ([]byte, error)) (string, error) {
	if goos != "linux" {
		return "127.0.0.1", nil
	}
	if info, err := docker(ctx, "info", "--format", "{{.OperatingSystem}}"); err == nil && strings.Contains(string(info), "Docker Desktop") {
		return "127.0.0.1", nil
	}
	out, err := docker(ctx, "network", "inspect", "bridge", "--format", "{{(index .IPAM.Config 0).Gateway}}")
	if err != nil {
		return "", fmt.Errorf("finding Docker's bridge address: %w", err)
	}
	ip := net.ParseIP(strings.TrimSpace(string(out)))
	if ip == nil || ip.IsUnspecified() {
		return "", fmt.Errorf("docker's bridge gateway %q is not a usable address", strings.TrimSpace(string(out)))
	}
	return ip.String(), nil
}
