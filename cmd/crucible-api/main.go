package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"crucible/internal/agenthub"
	"crucible/internal/auth"
	"crucible/internal/db"
	"crucible/internal/gitsync"
	"crucible/internal/httpapi"
	"crucible/internal/labs"
	"crucible/internal/learn"
)

func main() {
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, nil)))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx); err != nil {
		slog.Error("crucible-api failed", "err", err)
		os.Exit(1)
	}
}

func run(ctx context.Context) error {
	pool, err := db.Open(ctx, must("DATABASE_URL"))
	if err != nil {
		return err
	}
	defer pool.Close()

	syncer := gitsync.New(env("CRUCIBLE_DATA_DIR", "/data"), must("CRUCIBLE_PLATFORM_REPO"), env("CRUCIBLE_PLATFORM_BRANCH", "main"), slog.Default())
	if err := syncer.SyncOnce(ctx); err != nil {
		slog.Warn("initial git sync failed; retrying in the background", "err", err)
	}
	every, err := time.ParseDuration(env("CRUCIBLE_SYNC_INTERVAL", "60s"))
	if err != nil {
		return fmt.Errorf("CRUCIBLE_SYNC_INTERVAL: %w", err)
	}
	go syncer.Run(ctx, every)

	public := strings.TrimRight(env("CRUCIBLE_PUBLIC_URL", "http://localhost:8080"), "/")
	store := auth.Store{DB: pool}
	var oidcH *auth.OIDC
	for attempt := 1; ; attempt++ { // the IdP may still be starting
		oidcH, err = auth.NewOIDC(ctx, auth.OIDCConfig{Issuer: must("OIDC_ISSUER"), DiscoveryURL: os.Getenv("OIDC_DISCOVERY_URL"),
			ClientID: must("OIDC_CLIENT_ID"), ClientSecret: os.Getenv("OIDC_CLIENT_SECRET"), RedirectURL: public + "/auth/callback"},
			store, strings.HasPrefix(public, "https://"))
		if err == nil || attempt == 30 {
			break
		}
		slog.Info("waiting for the identity provider", "attempt", attempt, "err", err)
		time.Sleep(2 * time.Second)
	}
	if err != nil {
		return err
	}

	hub := agenthub.New()
	learnSvc := &learn.Service{DB: pool, State: syncer.Current}
	labSvc := &labs.Service{DB: pool, Learn: learnSvc, Runners: map[string]labs.Runner{"local": labs.LocalRunner{Hub: hub}},
		Now: time.Now, Log: slog.Default()}
	go labSvc.RunSweeper(ctx, 15*time.Second)

	srv := &http.Server{
		Addr: env("CRUCIBLE_ADDR", ":8080"),
		Handler: httpapi.NewRouter(httpapi.Deps{Auth: store, OIDC: oidcH, Sync: syncer, Learn: learnSvc, Labs: labSvc, Hub: hub,
			PublicURL: public, HookSecret: os.Getenv("CRUCIBLE_GIT_HOOK_SECRET"), WebDir: env("CRUCIBLE_WEB_DIR", "web/dist")}),
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdown)
	}()
	slog.Info("crucible-api listening. The forge is lit", "addr", srv.Addr, "public_url", public)
	if err := srv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func must(k string) string {
	v := os.Getenv(k)
	if v == "" {
		slog.Error("missing required environment variable", "name", k)
		os.Exit(2)
	}
	return v
}
