// Command crucible-agent runs laptop labs (Docker compose) and cluster labs (Helm on a cluster of your kubeconfig).
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"crucible/internal/agent"
)

func main() {
	home, _ := os.UserHomeDir()
	server := flag.String("server", os.Getenv("CRUCIBLE_SERVER"), "Crucible URL, e.g. https://crucible.example.com")
	token := flag.String("token", os.Getenv("CRUCIBLE_TOKEN"), "pairing token (prefer the CRUCIBLE_TOKEN env var: flags are visible to other local users)")
	labs := flag.String("labs-dir", filepath.Join(home, ".crucible", "labs"), "where lab files are unpacked")
	kubeconfig := flag.String("kubeconfig", "", "kubeconfig for cluster labs (default: $KUBECONFIG, else ~/.kube/config)")
	flag.Parse()
	if *server == "" || *token == "" {
		fmt.Fprintln(os.Stderr, "usage: CRUCIBLE_TOKEN=TOKEN crucible-agent --server URL")
		os.Exit(2)
	}
	if err := checkServer(*server); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	if _, err := exec.LookPath("docker"); err != nil {
		fmt.Fprintln(os.Stderr, "crucible-agent needs Docker with the compose plugin on your PATH")
		os.Exit(1)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	run := func(ctx context.Context, args ...string) ([]byte, error) {
		return exec.CommandContext(ctx, "docker", args...).Output()
	}
	cluster := &agent.Cluster{Dir: *labs, Kubeconfig: *kubeconfig, Bind: func(ctx context.Context) (string, error) {
		return agent.BindAddr(ctx, runtime.GOOS, run)
	}}
	kube := &agent.Watcher{Kubeconfig: *kubeconfig, Every: 5 * time.Second}
	go kube.Run(ctx)
	c := &agent.Client{Server: strings.TrimRight(*server, "/"), Token: *token, Exec: &agent.Labs{Compose: agent.Compose{Dir: *labs}, Cluster: cluster},
		Kube: kube, Log: slog.Default()}
	if err := c.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		slog.Error("agent stopped", "err", err)
		os.Exit(1)
	}
}

// checkServer allows plain http only to this machine: the pairing token must not cross a network in clear text.
func checkServer(server string) error {
	u, err := url.Parse(server)
	if err != nil || u.Host == "" {
		return errors.New("--server must be a URL like https://crucible.example.com")
	}
	switch {
	case u.Scheme == "https":
		return nil
	case u.Scheme == "http" && (u.Hostname() == "localhost" || u.Hostname() == "127.0.0.1" || u.Hostname() == "::1"):
		return nil
	}
	return errors.New("--server must use https:// (plain http:// is only allowed for localhost)")
}
