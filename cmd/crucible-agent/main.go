// Command crucible-agent runs local labs on a trainee's laptop (macOS, Linux, or Windows via WSL2).
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"crucible/internal/agent"
)

func main() {
	home, _ := os.UserHomeDir()
	server := flag.String("server", os.Getenv("CRUCIBLE_SERVER"), "Crucible URL, e.g. https://crucible.example.com")
	token := flag.String("token", os.Getenv("CRUCIBLE_TOKEN"), "pairing token from the “Connect your laptop” page")
	labs := flag.String("labs-dir", filepath.Join(home, ".crucible", "labs"), "where lab files are unpacked")
	flag.Parse()
	if *server == "" || *token == "" {
		fmt.Fprintln(os.Stderr, "usage: crucible-agent --server URL --token TOKEN")
		os.Exit(2)
	}
	if !strings.HasPrefix(*server, "http://") && !strings.HasPrefix(*server, "https://") {
		fmt.Fprintln(os.Stderr, "--server must start with http:// or https://")
		os.Exit(2)
	}
	if _, err := exec.LookPath("docker"); err != nil {
		fmt.Fprintln(os.Stderr, "crucible-agent needs Docker with the compose plugin on your PATH")
		os.Exit(1)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	c := &agent.Client{Server: strings.TrimRight(*server, "/"), Token: *token, Exec: agent.Compose{Dir: *labs}, Log: slog.Default()}
	if err := c.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		slog.Error("agent stopped", "err", err)
		os.Exit(1)
	}
}
