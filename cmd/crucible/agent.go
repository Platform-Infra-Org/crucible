// This file is `crucible agent`: it runs laptop labs (Docker compose) and cluster labs (Helm on a cluster of your kubeconfig).
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
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

// agentCmd is `crucible agent`.
func agentCmd(args []string, out, errw io.Writer) int {
	if len(args) > 0 && args[0] == "cluster" {
		return clusterCmd(args[1:], agent.NewKind(), out, errw)
	}
	home, _ := os.UserHomeDir()
	fs := flag.NewFlagSet("agent", flag.ContinueOnError)
	fs.SetOutput(errw)
	server := fs.String("server", os.Getenv("CRUCIBLE_SERVER"), "Crucible URL, e.g. https://crucible.example.com")
	token := fs.String("token", os.Getenv("CRUCIBLE_TOKEN"), "pairing token (prefer the CRUCIBLE_TOKEN env var: flags are visible to other local users)")
	labs := fs.String("labs-dir", filepath.Join(home, ".crucible", "labs"), "where lab files are unpacked")
	kubeconfig := fs.String("kubeconfig", "", "kubeconfig for cluster labs (default: $KUBECONFIG, else ~/.kube/config)")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if *server == "" || *token == "" {
		fmt.Fprintln(errw, "usage: CRUCIBLE_TOKEN=TOKEN crucible agent --server URL")
		return 2
	}
	if err := checkServer(*server); err != nil {
		fmt.Fprintln(errw, err)
		return 2
	}
	if _, err := exec.LookPath("docker"); err != nil {
		fmt.Fprintln(errw, "crucible agent needs Docker with the compose plugin on your PATH")
		return 1
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
		return 1
	}
	return 0
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

// clusterCmd runs `cluster up|down [--name crucible]` and returns the exit code.
func clusterCmd(args []string, k agent.Kind, out, errw io.Writer) int {
	if len(args) == 0 || (args[0] != "up" && args[0] != "down") {
		fmt.Fprintln(errw, "usage: crucible agent cluster up|down [--name crucible]")
		return 2
	}
	fs := flag.NewFlagSet("cluster "+args[0], flag.ContinueOnError)
	fs.SetOutput(errw)
	name := fs.String("name", "crucible", "cluster name")
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}
	do := agent.ClusterUp
	if args[0] == "down" {
		do = agent.ClusterDown
	}
	if err := do(k, *name, out); err != nil {
		fmt.Fprintln(errw, err)
		return 1
	}
	return 0
}
