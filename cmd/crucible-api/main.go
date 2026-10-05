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
	_ "time/tzdata" // schedules name IANA zones; the runtime image has no zoneinfo

	"github.com/riverqueue/river"

	"crucible/internal/agenthub"
	"crucible/internal/auth"
	"crucible/internal/content"
	"crucible/internal/db"
	"crucible/internal/gitsync"
	"crucible/internal/httpapi"
	"crucible/internal/jobs"
	"crucible/internal/labs"
	"crucible/internal/learn"
	"crucible/internal/notify"
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
	quizSecret := os.Getenv("CRUCIBLE_QUIZ_SECRET")
	if quizSecret == "" {
		slog.Warn("CRUCIBLE_QUIZ_SECRET is not set; using a fixed development value. Set it in production so learners cannot predict quiz choice ids")
		quizSecret = "crucible-dev-quiz-secret"
	}
	learnSvc := &learn.Service{DB: pool, State: syncer.Current, QuizSecret: quizSecret}
	notifySvc := &notify.Service{DB: pool, State: syncer.Current, PublicURL: public, Log: slog.Default(),
		SMTP: notify.SMTPConfig{Addr: os.Getenv("CRUCIBLE_SMTP_ADDR"), From: env("CRUCIBLE_SMTP_FROM", "crucible@localhost"),
			Username: os.Getenv("CRUCIBLE_SMTP_USERNAME"), Password: os.Getenv("CRUCIBLE_SMTP_PASSWORD")}}
	if notifySvc.SMTP.Addr == "" {
		slog.Warn("CRUCIBLE_SMTP_ADDR is not set: email notifications are off (Slack/Teams webhooks still work)")
	}
	labSvc := &labs.Service{Notify: notifySvc, DB: pool, Learn: learnSvc, Runners: map[string]labs.Runner{"local": labs.LocalRunner{Hub: hub}},
		Now: time.Now, Log: slog.Default()}
	hub.OnHello = func(userID int64, liveIDs []string) { labSvc.ReconcileAgent(ctx, userID, liveIDs) }
	workers := river.NewWorkers()
	river.AddWorker(workers, &labs.SweepWorker{S: labSvc})
	river.AddWorker(workers, &notify.EmailWorker{S: notifySvc})
	river.AddWorker(workers, &notify.WebhookWorker{S: notifySvc})
	riverLog := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	jobClient, err := jobs.New(pool, workers, []jobs.Periodic{{Every: 15 * time.Second, Args: labs.SweepArgs{}}}, riverLog)
	if err != nil {
		return err
	}
	notifySvc.Jobs = jobClient
	syncer.OnProblem = func(key string, probs []content.Problem) { notifySvc.ReportSyncProblem(ctx, key, probs) }
	go syncer.Run(ctx, every)
	if err := jobClient.Start(ctx); err != nil {
		return err
	}
	defer func() {
		stop, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = jobClient.Stop(stop)
	}()

	srv := &http.Server{
		Addr: env("CRUCIBLE_ADDR", ":8080"),
		Handler: httpapi.NewRouter(httpapi.Deps{Auth: store, OIDC: oidcH, Sync: syncer, Learn: learnSvc, Labs: labSvc, Notify: notifySvc, Hub: hub,
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
