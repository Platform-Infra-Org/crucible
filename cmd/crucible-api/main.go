package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
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
	"crucible/internal/edits"
	"crucible/internal/gitsync"
	"crucible/internal/httpapi"
	"crucible/internal/infracost"
	"crucible/internal/jobs"
	"crucible/internal/journey"
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

// previewGuard fails closed: preview mode (an admin login by token, OIDC off) starts only when CRUCIBLE_PUBLIC_URL is
// explicitly a loopback http URL and no OIDC variable is set, so the default public URL or a stray token on a real
// deployment can never enable it. A no-op when CRUCIBLE_PREVIEW_TOKEN is unset.
func previewGuard(getenv func(string) string) error {
	token := getenv("CRUCIBLE_PREVIEW_TOKEN")
	if token == "" {
		return nil
	}
	public := strings.TrimRight(getenv("CRUCIBLE_PUBLIC_URL"), "/")
	if public == "" {
		return errors.New("CRUCIBLE_PUBLIC_URL must be set explicitly")
	}
	for _, k := range []string{"OIDC_ISSUER", "OIDC_DISCOVERY_URL", "OIDC_CLIENT_ID", "OIDC_CLIENT_SECRET"} {
		if getenv(k) != "" {
			return fmt.Errorf("%s is set: a deployment with OIDC never runs in preview mode", k)
		}
	}
	return auth.PreviewAllowed(public, token)
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
	if email := strings.ToLower(strings.TrimSpace(os.Getenv("CRUCIBLE_BOOTSTRAP_ADMIN"))); email != "" {
		// Once per deployment, only while admins.yaml names no admin; sign-in still needs the IdP to verify this email.
		if ok, err := configapi.BootstrapAdmin(ctx, pool, writer, email); err != nil {
			slog.Warn("seeding the bootstrap admin failed; add them to admins.yaml in git", "err", err)
		} else if ok {
			slog.Info("seeded admins.yaml with the bootstrap admin", "email", email)
			_ = syncer.SyncOnce(ctx)
		}
	}
	every, err := time.ParseDuration(env("CRUCIBLE_SYNC_INTERVAL", "60s"))
	if err != nil {
		return fmt.Errorf("CRUCIBLE_SYNC_INTERVAL: %w", err)
	}

	public := strings.TrimRight(env("CRUCIBLE_PUBLIC_URL", "http://localhost:8080"), "/")
	store := auth.Store{DB: pool}
	var oidcH *auth.OIDC
	previewToken := os.Getenv("CRUCIBLE_PREVIEW_TOKEN")
	if previewToken != "" {
		if err := previewGuard(os.Getenv); err != nil {
			return fmt.Errorf("preview mode: %w", err)
		}
		slog.Warn("PREVIEW MODE: OIDC is off and /auth/preview signs in with the preview token. Only crucible preview sets this")
	} else {
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
	}

	hub := agenthub.New()
	quizSecret := os.Getenv("CRUCIBLE_QUIZ_SECRET")
	if quizSecret == "" {
		slog.Warn("CRUCIBLE_QUIZ_SECRET is not set; using a fixed development value. Set it in production so learners cannot predict quiz choice ids")
		quizSecret = "crucible-dev-quiz-secret"
	}
	cfgSvc := &configapi.Service{DB: pool, State: syncer.Current, Writer: writer, Resync: syncer.SyncOnce,
		Changes: syncer.Changes, CheckPin: syncer.CheckPin}
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
		runners["cluster"], estimators["cluster"] = cr, labs.PlatformRate{State: syncer.Current, Override: rates}
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
		est, err := awsSetup(ctx, mode, ar, rates, os.Getenv, func(ctx context.Context, c awscloud.Config) (awscloud.Cloud, error) {
			return awscloud.New(ctx, c)
		})
		if err != nil {
			return err
		}
		if est != nil {
			estimators["aws"] = est
		}
		runners["aws"], cloud = ar, ar.Cloud
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
	learnSvc.Notify = notifySvc
	labSvc.Scoring, labSvc.Blobs = scoreSvc, blobs
	hub.OnHello = func(userID int64, liveIDs []string) { labSvc.ReconcileAgent(ctx, userID, liveIDs) }
	workers := river.NewWorkers()
	river.AddWorker(workers, &labs.SweepWorker{S: labSvc})
	river.AddWorker(workers, &labs.BudgetWorker{S: labSvc})
	river.AddWorker(workers, &labs.ReapWorker{S: labSvc})
	river.AddWorker(workers, &labs.CostWorker{S: labSvc})
	river.AddWorker(workers, &notify.EmailWorker{S: notifySvc})
	river.AddWorker(workers, &notify.WebhookWorker{S: notifySvc})
	riverLog := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	jobClient, err := jobs.New(pool, workers, periodicJobs(), riverLog)
	if err != nil {
		return err
	}
	notifySvc.Jobs, labSvc.Jobs = jobClient, jobClient
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

	// One ContentRepo per repo and branch for the whole process: its lock serializes git work on its clone, so a second
	// value on the same dir would race it. A training moved to another repo gets a new value and a new dir.
	var repoMu sync.Mutex
	repos := map[string]*gitsync.ContentRepo{}
	editsSvc := &edits.Service{DB: pool, State: syncer.Current, Notify: notifySvc, Resync: syncer.SyncOnce, Log: slog.Default(),
		Repo: func(id string) *gitsync.ContentRepo {
			st := syncer.Current()
			if st == nil || st.Platform == nil {
				return nil
			}
			ref, ok := st.Platform.Trainings[id]
			if !ok {
				return nil
			}
			sum := sha256.Sum256([]byte(ref.Repo + "\x00" + ref.Branch))
			key := hex.EncodeToString(sum[:8])
			repoMu.Lock()
			defer repoMu.Unlock()
			if repos[key] == nil {
				repos[key] = &gitsync.ContentRepo{URL: ref.Repo, Branch: ref.Branch, Dir: filepath.Join(env("CRUCIBLE_DATA_DIR", "/data"), "edits", key),
					Name: writer.Name, Email: writer.Email}
			}
			return repos[key]
		}}

	srv := &http.Server{
		Addr: env("CRUCIBLE_ADDR", ":8080"),
		Handler: httpapi.NewRouter(httpapi.Deps{Auth: store, OIDC: oidcH, Sync: syncer, Learn: learnSvc, Labs: labSvc, Scoring: scoreSvc, Notify: notifySvc, Config: cfgSvc, Hub: hub,
			Journey: &journey.Service{DB: pool, Learn: learnSvc, Now: time.Now}, Edits: editsSvc,
			PublicURL: public, HookSecret: os.Getenv("CRUCIBLE_GIT_HOOK_SECRET"), WebDir: env("CRUCIBLE_WEB_DIR", "web/dist"), PreviewToken: previewToken}),
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

// awsSetup gives the aws runner its cloud and returns the aws lab estimator (nil: no estimate, so no requests).
// Dryrun never calls newCloud, the real SDK client. CRUCIBLE_INFRACOST=off prices labs from
// CRUCIBLE_DEV_LAB_USD_PER_HOUR, $0 when unset: no budget stop, no over-cap routing, so dryrun only.
func awsSetup(ctx context.Context, mode string, ar *labs.AWSRunner, rates labs.Estimator, getenv func(string) string,
	newCloud func(context.Context, awscloud.Config) (awscloud.Cloud, error)) (labs.Estimator, error) {
	off := getenv("CRUCIBLE_INFRACOST") == "off"
	if off && mode != "dryrun" {
		return nil, errors.New("CRUCIBLE_INFRACOST=off is only allowed with CRUCIBLE_AWS_LABS=dryrun: real aws labs need an infracost estimate")
	}
	if mode == "dryrun" {
		fake := &awscloud.Fake{}
		ar.Cloud, ar.DryRun = fake, fake
		slog.Warn("CRUCIBLE_AWS_LABS=dryrun: aws labs run without AWS (terraform pods only unpack the module; a fake lab account stands in). Development only")
	} else {
		if ar.StateBucket == "" || ar.StateRegion == "" {
			return nil, errors.New("CRUCIBLE_AWS_LABS=1 needs CRUCIBLE_AWS_STATE_BUCKET and CRUCIBLE_AWS_STATE_REGION")
		}
		c, err := newCloud(ctx, awscloud.Config{LabRoleARN: getenv("CRUCIBLE_AWS_LAB_ROLE_ARN"), OpsRoleARN: getenv("CRUCIBLE_AWS_OPS_ROLE_ARN")})
		if err != nil {
			return nil, fmt.Errorf("aws labs: %w", err)
		}
		ar.Cloud = c
	}
	_, cliErr := exec.LookPath("infracost")
	switch {
	case off:
		slog.Warn("CRUCIBLE_INFRACOST=off: aws labs are priced from CRUCIBLE_DEV_LAB_USD_PER_HOUR")
		return rates, nil
	case cliErr != nil || getenv("INFRACOST_API_KEY") == "":
		slog.Warn("aws labs need the infracost CLI and INFRACOST_API_KEY for an estimate: until then they show no cost estimate and cannot be requested")
		return nil, nil
	}
	return &labs.InfracostEstimator{Run: infracost.Exec}, nil
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

// periodicJobs: the reaper runs hourly so leftovers of a leaked one-hour lab session live at most about 2 h after
// End (tagging and CloudTrail lookups are free); Cost Explorer ingestion costs money per call, so it stays at 6 h.
func periodicJobs() []jobs.Periodic {
	return []jobs.Periodic{{Every: 15 * time.Second, Args: labs.SweepArgs{}}, {Every: 5 * time.Minute, Args: labs.BudgetArgs{}},
		{Every: time.Hour, Args: labs.ReapArgs{}}, {Every: 6 * time.Hour, Args: labs.CostArgs{}}}
}
