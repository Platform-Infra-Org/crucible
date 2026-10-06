package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	_ "time/tzdata" // schedules name IANA zones; the runtime image has no zoneinfo

	"github.com/riverqueue/river"
	"k8s.io/client-go/tools/clientcmd"

	"crucible/internal/agenthub"
	"crucible/internal/auth"
	"crucible/internal/awscloud"
	"crucible/internal/blob"
	"crucible/internal/configapi"
	"crucible/internal/content"
	"crucible/internal/db"
	"crucible/internal/gitsync"
	"crucible/internal/httpapi"
	"crucible/internal/infracost"
	"crucible/internal/jobs"
	"crucible/internal/labs"
	"crucible/internal/learn"
	"crucible/internal/notify"
	"crucible/internal/scoring"
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
	writer := &gitsync.Writer{URL: must("CRUCIBLE_PLATFORM_REPO"), Branch: env("CRUCIBLE_PLATFORM_BRANCH", "main"),
		Dir:  filepath.Join(env("CRUCIBLE_DATA_DIR", "/data"), "writer"),
		Name: env("CRUCIBLE_GIT_BOT_NAME", "Crucible"), Email: env("CRUCIBLE_GIT_BOT_EMAIL", "crucible@localhost")}
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
	cfgSvc := &configapi.Service{DB: pool, State: syncer.Current, Writer: writer, Resync: syncer.SyncOnce}
	learnSvc := &learn.Service{DB: pool, State: syncer.Current, Versions: syncer.Version, QuizSecret: quizSecret}
	notifySvc := &notify.Service{DB: pool, State: syncer.Current, PublicURL: public, Log: slog.Default(),
		SMTP: notify.SMTPConfig{Addr: os.Getenv("CRUCIBLE_SMTP_ADDR"), From: env("CRUCIBLE_SMTP_FROM", "crucible@localhost"),
			Username: os.Getenv("CRUCIBLE_SMTP_USERNAME"), Password: os.Getenv("CRUCIBLE_SMTP_PASSWORD")}}
	if notifySvc.SMTP.Addr == "" {
		slog.Warn("CRUCIBLE_SMTP_ADDR is not set: email notifications are off (Slack/Teams webhooks still work)")
	}
	rates, err := labs.ParseRates(os.Getenv("CRUCIBLE_DEV_LAB_USD_PER_HOUR"))
	if err != nil {
		return fmt.Errorf("CRUCIBLE_DEV_LAB_USD_PER_HOUR: %w", err)
	}
	if len(rates) > 0 {
		slog.Warn("CRUCIBLE_DEV_LAB_USD_PER_HOUR is set: these local labs are priced for testing approvals", "rates", rates)
	}
	runners := map[string]labs.Runner{"local": labs.LocalRunner{Hub: hub}}
	estimators := map[string]labs.Estimator{"local": rates}
	if os.Getenv("CRUCIBLE_CLUSTER_LABS") == "1" {
		// KUBECONFIG when set (dev), else the pod's service account (in-cluster).
		cfg, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(clientcmd.NewDefaultClientConfigLoadingRules(),
			&clientcmd.ConfigOverrides{}).ClientConfig()
		if err != nil {
			return fmt.Errorf("cluster labs: %w", err)
		}
		// Dev clusters only: off unless explicitly set to exactly "1", and loud when on.
		privileged := os.Getenv("CRUCIBLE_CLUSTER_PRIVILEGED") == "1"
		if privileged {
			slog.Warn("CRUCIBLE_CLUSTER_PRIVILEGED=1: cluster labs run as privileged pods without sysbox. Use this on development clusters only")
		}
		cr, err := labs.NewClusterRunner(cfg, privileged)
		if err != nil {
			return fmt.Errorf("cluster labs: %w", err)
		}
		runners["cluster"], estimators["cluster"] = cr, rates // M4 ruling 6: priced like local labs until M6
		slog.Info("cluster labs enabled", "api", cfg.Host, "privileged", privileged)
	}
	var cloud awscloud.Cloud
	var awsRegions []string
	switch mode := os.Getenv("CRUCIBLE_AWS_LABS"); mode {
	case "":
	case "1", "dryrun":
		cr, ok := runners["cluster"].(*labs.ClusterRunner)
		if !ok {
			return errors.New("CRUCIBLE_AWS_LABS needs CRUCIBLE_CLUSTER_LABS=1: the workspace and terraform pods run in the cluster")
		}
		ar := &labs.AWSRunner{Cluster: cr, StateBucket: os.Getenv("CRUCIBLE_AWS_STATE_BUCKET"), StateRegion: os.Getenv("CRUCIBLE_AWS_STATE_REGION"),
			WorkspaceImage: env("CRUCIBLE_AWS_WORKSPACE_IMAGE", labs.DefaultWorkspaceImage),
			TerraformImage: env("CRUCIBLE_TERRAFORM_IMAGE", labs.DefaultTerraformImage)}
		for r := range strings.SplitSeq(env("CRUCIBLE_AWS_LAB_REGIONS", "eu-west-1"), ",") {
			if r = strings.TrimSpace(r); r != "" {
				awsRegions = append(awsRegions, r)
			}
		}
		if mode == "dryrun" { // never constructs the real SDK client
			fake := &awscloud.Fake{}
			ar.Cloud, ar.DryRun = fake, fake
			slog.Warn("CRUCIBLE_AWS_LABS=dryrun: aws labs run without AWS (terraform pods only unpack the module; a fake lab account stands in). Development only")
		} else {
			if ar.StateBucket == "" || ar.StateRegion == "" {
				return errors.New("CRUCIBLE_AWS_LABS=1 needs CRUCIBLE_AWS_STATE_BUCKET and CRUCIBLE_AWS_STATE_REGION")
			}
			if ar.Cloud, err = awscloud.New(ctx, awscloud.Config{LabRoleARN: os.Getenv("CRUCIBLE_AWS_LAB_ROLE_ARN"),
				OpsRoleARN: os.Getenv("CRUCIBLE_AWS_OPS_ROLE_ARN")}); err != nil {
				return fmt.Errorf("aws labs: %w", err)
			}
		}
		runners["aws"], cloud = ar, ar.Cloud
		_, cliErr := exec.LookPath("infracost")
		switch {
		case os.Getenv("CRUCIBLE_INFRACOST") == "off":
			estimators["aws"] = rates
			slog.Warn("CRUCIBLE_INFRACOST=off: aws labs are priced from CRUCIBLE_DEV_LAB_USD_PER_HOUR")
		case cliErr != nil || os.Getenv("INFRACOST_API_KEY") == "":
			slog.Warn("aws labs need the infracost CLI and INFRACOST_API_KEY for an estimate: until then they show no cost estimate and cannot be requested")
		default:
			estimators["aws"] = &labs.InfracostEstimator{Run: infracost.Exec}
		}
		slog.Info("aws labs enabled", "mode", mode, "regions", awsRegions)
	default:
		return fmt.Errorf("CRUCIBLE_AWS_LABS must be 1, dryrun or empty, not %q", mode)
	}
	labSvc := &labs.Service{Notify: notifySvc, DB: pool, Learn: learnSvc, Runners: runners, Estimators: estimators,
		Now: time.Now, Log: slog.Default(), Cloud: cloud, AWSRegions: awsRegions}
	var blobs blob.Store = blob.Disk{Dir: filepath.Join(env("CRUCIBLE_DATA_DIR", "/data"), "blobs")}
	if bucket := os.Getenv("CRUCIBLE_BLOB_BUCKET"); bucket != "" {
		s3store, err := blob.NewS3(ctx, bucket, os.Getenv("CRUCIBLE_BLOB_REGION"), os.Getenv("CRUCIBLE_BLOB_ENDPOINT"))
		if err != nil {
			return fmt.Errorf("blob storage: %w", err)
		}
		blobs = s3store
	} else {
		slog.Warn("CRUCIBLE_BLOB_BUCKET is not set: uploads and terminal transcripts are kept under CRUCIBLE_DATA_DIR/blobs; use a persistent volume or S3 in production")
	}
	scoreSvc := &scoring.Service{DB: pool, Blobs: blobs, State: syncer.Current, Notify: notifySvc, Quiz: learnSvc, Labs: labSvc,
		Log: slog.Default(), Now: time.Now}
	learnSvc.Scoring = scoreSvc
	labSvc.Scoring, labSvc.Blobs = scoreSvc, blobs
	hub.OnHello = func(userID int64, liveIDs []string) { labSvc.ReconcileAgent(ctx, userID, liveIDs) }
	workers := river.NewWorkers()
	river.AddWorker(workers, &labs.SweepWorker{S: labSvc})
	river.AddWorker(workers, &labs.BudgetWorker{S: labSvc})
	river.AddWorker(workers, &notify.EmailWorker{S: notifySvc})
	river.AddWorker(workers, &notify.WebhookWorker{S: notifySvc})
	riverLog := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	jobClient, err := jobs.New(pool, workers, []jobs.Periodic{{Every: 15 * time.Second, Args: labs.SweepArgs{}}, {Every: 5 * time.Minute, Args: labs.BudgetArgs{}}}, riverLog)
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
		Handler: httpapi.NewRouter(httpapi.Deps{Auth: store, OIDC: oidcH, Sync: syncer, Learn: learnSvc, Labs: labSvc, Scoring: scoreSvc, Notify: notifySvc, Config: cfgSvc, Hub: hub,
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

var (
	_ scoring.Labs     = (*labs.Service)(nil)
	_ scoring.Progress = (*learn.Service)(nil)
)
