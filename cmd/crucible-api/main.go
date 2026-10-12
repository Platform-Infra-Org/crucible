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
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
	_ "time/tzdata" // schedules name IANA zones; the runtime image has no zoneinfo

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"k8s.io/client-go/tools/clientcmd"

	"crucible/internal/agenthub"
	"crucible/internal/auth"
	"crucible/internal/authoring"
	"crucible/internal/awscloud"
	"crucible/internal/blob"
	"crucible/internal/config"
	"crucible/internal/configapi"
	"crucible/internal/content"
	"crucible/internal/db"
	"crucible/internal/docs"
	"crucible/internal/edits"
	"crucible/internal/gitsync"
	"crucible/internal/httpapi"
	"crucible/internal/jobs"
	"crucible/internal/journey"
	"crucible/internal/labs"
	"crucible/internal/learn"
	"crucible/internal/notify"
	"crucible/internal/org"
	"crucible/internal/pricing"
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

// configSource checks the environment for the configuration store. Configuration lives in Postgres only; an old
// git-mode deployment that still sets CRUCIBLE_PLATFORM_REPO would otherwise start on an empty database without a
// word, so it is refused with what to do instead.
func configSource(getenv func(string) string) error {
	if strings.TrimSpace(getenv("CRUCIBLE_PLATFORM_REPO")) != "" {
		return errors.New("CRUCIBLE_PLATFORM_REPO is no longer read: configuration lives in Postgres. Remove it, start with " +
			"CRUCIBLE_BOOTSTRAP_ADMIN set and configure the instance in the UI, or import a platform directory once with CRUCIBLE_SEED_DIR")
	}
	return nil
}

// seed imports CRUCIBLE_SEED_DIR (a platform directory in the YAML layout, e.g. examples/platform) into an empty
// database, once; see org.Store.Seed. A seed that does not load or does not validate stops the server: it is set on
// purpose, and half an instance is worse than none.
func seed(ctx context.Context, s *org.Store, dir string) error {
	if strings.TrimSpace(dir) == "" {
		return nil
	}
	p, err := config.Load(dir)
	if err != nil {
		return fmt.Errorf("CRUCIBLE_SEED_DIR: %w", err)
	}
	ok, err := s.Seed(ctx, p)
	if err != nil {
		return fmt.Errorf("CRUCIBLE_SEED_DIR: %w", err)
	}
	if ok {
		slog.Info("seeded the configuration", "dir", dir, "teams", len(p.Teams), "trainings", len(p.Trainings))
	} else {
		slog.Info("CRUCIBLE_SEED_DIR is set but this database is already configured or was seeded before; leaving it alone", "dir", dir)
	}
	return nil
}

// seedBootstrapAdmin seeds the first admin before the first sync, so the first snapshot names them.
func seedBootstrapAdmin(ctx context.Context, pool *pgxpool.Pool, email string) {
	email = strings.ToLower(strings.TrimSpace(email))
	if email == "" {
		return
	}
	if ok, err := configapi.BootstrapAdmin(ctx, pool, email); err != nil {
		slog.Warn("seeding the bootstrap admin failed; add an admin from the admin page", "err", err)
	} else if ok {
		slog.Info("seeded the bootstrap admin", "email", email)
	}
}

func run(ctx context.Context) error {
	pool, err := db.Open(ctx, must("DATABASE_URL"))
	if err != nil {
		return err
	}
	defer pool.Close()

	gitsync.AllowFileTransport = gitsync.AllowFileFromEnv(os.Getenv)
	if err := configSource(os.Getenv); err != nil {
		return err
	}
	orgStore := &org.Store{DB: pool}
	if err := seed(ctx, orgStore, os.Getenv("CRUCIBLE_SEED_DIR")); err != nil {
		return err
	}
	seedBootstrapAdmin(ctx, pool, os.Getenv("CRUCIBLE_BOOTSTRAP_ADMIN"))
	dataDir := env("CRUCIBLE_DATA_DIR", "/data")
	syncer := gitsync.New(dataDir, orgStore.Platform, slog.Default())
	if err := syncer.SyncOnce(ctx); err != nil {
		slog.Warn("initial sync failed; retrying in the background", "err", err)
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
	cfgSvc := &configapi.Service{DB: pool, State: syncer.Current, Changes: syncer.Changes}
	orgAPI := org.APIDeps{Refresh: syncer.SyncOnce, CheckPin: syncer.CheckPin, Platform: func() *config.Platform {
		if st := syncer.Current(); st != nil {
			return st.Platform
		}
		return nil
	}, Content: func(team, training string) (title, running, head string) {
		title = training
		if st := syncer.Current(); st != nil {
			if t, _ := st.ProgramTraining(team, training); t != nil {
				title = t.Title
			} else if t := st.Training(training, st.Heads[training]); t != nil {
				title = t.Title
			}
			running, head = st.ProgramSHAs[team+"/"+training], st.Heads[training]
		}
		return
	}}
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
	if img := os.Getenv("CRUCIBLE_TOOLBOX_IMAGE"); img != "" {
		runners["cluster"], estimators["cluster"] = labs.AgentClusterRunner{Hub: hub, Toolbox: img}, labs.FixedRates{} // free: the trainee's cluster
		slog.Info("cluster labs enabled (on trainees' own clusters)", "toolbox", img)
	}
	var kube *labs.ClusterRunner
	legacy := os.Getenv("CRUCIBLE_CLUSTER_LABS") == "1"
	if legacy {
		slog.Warn("CRUCIBLE_CLUSTER_LABS is retired: cluster labs run on trainees' own clusters (CRUCIBLE_TOOLBOX_IMAGE). Crucible's cluster is used for AWS labs (CRUCIBLE_LAB_CLUSTER=1) and to clean up old cluster labs")
	}
	if os.Getenv("CRUCIBLE_LAB_CLUSTER") == "1" || legacy || os.Getenv("CRUCIBLE_AWS_LABS") != "" {
		// KUBECONFIG when set (dev), else the pod's service account (in-cluster).
		cfg, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(clientcmd.NewDefaultClientConfigLoadingRules(),
			&clientcmd.ConfigOverrides{}).ClientConfig()
		if err != nil {
			return fmt.Errorf("lab cluster: %w", err)
		}
		privileged := os.Getenv("CRUCIBLE_CLUSTER_PRIVILEGED") == "1"
		if privileged {
			slog.Warn("CRUCIBLE_CLUSTER_PRIVILEGED=1: AWS lab workspaces run as privileged pods without sysbox. Use this on development clusters only")
		}
		if kube, err = labs.NewClusterRunner(cfg, privileged); err != nil {
			return fmt.Errorf("lab cluster: %w", err)
		}
	}
	var cloud awscloud.Cloud
	var awsRegions []string
	var priceStore *pricing.Store
	var priceFetch *pricing.Fetcher
	switch mode := os.Getenv("CRUCIBLE_AWS_LABS"); mode {
	case "":
	case "1", "dryrun":
		ar := &labs.AWSRunner{Cluster: kube, StateBucket: os.Getenv("CRUCIBLE_AWS_STATE_BUCKET"), StateRegion: os.Getenv("CRUCIBLE_AWS_STATE_REGION"),
			WorkspaceImage: env("CRUCIBLE_AWS_WORKSPACE_IMAGE", labs.DefaultWorkspaceImage),
			TerraformImage: env("CRUCIBLE_TERRAFORM_IMAGE", labs.DefaultTerraformImage)}
		for r := range strings.SplitSeq(env("CRUCIBLE_AWS_LAB_REGIONS", "eu-west-1"), ",") {
			if r = strings.TrimSpace(r); r != "" {
				awsRegions = append(awsRegions, r)
			}
		}
		prices := &pricing.Store{DB: pool, Now: time.Now}
		est, fetch, err := awsSetup(ctx, mode, ar, rates, os.Getenv, func(ctx context.Context, c awscloud.Config) (awscloud.Cloud, error) {
			return awscloud.New(ctx, c)
		}, func(ctx context.Context) (pricing.Lookup, error) {
			s, err := prices.Snapshot(ctx)
			if err != nil {
				return nil, err // not a nil Snapshot in a non-nil Lookup
			}
			return s, nil
		})
		if err != nil {
			return err
		}
		if fetch == nil { // dry run: fixture prices, refreshed on every start, so metering prices the fake apply
			if err := prices.Replace(ctx, pricing.Fixture()); err != nil {
				return fmt.Errorf("dry-run prices: %w", err)
			}
		}
		estimators["aws"], priceStore, priceFetch = est, prices, fetch
		runners["aws"], cloud = ar, ar.Cloud
		slog.Info("aws labs enabled", "mode", mode, "regions", awsRegions)
	default:
		return fmt.Errorf("CRUCIBLE_AWS_LABS must be 1, dryrun or empty, not %q", mode)
	}
	labSvc := &labs.Service{Notify: notifySvc, DB: pool, Learn: learnSvc, Runners: runners, Estimators: estimators,
		Now: time.Now, Log: slog.Default(), Cloud: cloud, AWSRegions: awsRegions, Kube: kube, Prices: priceStore, PriceFetch: priceFetch}
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
	river.AddWorker(workers, &labs.PriceWorker{S: labSvc})
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
	editsSvc := &edits.Service{DB: pool, State: syncer.Current, Notify: notifySvc, Resync: syncer.SyncOnce, Version: syncer.Version, Log: slog.Default(),
		Repo: func(id string) *gitsync.ContentRepo {
			st := syncer.Current()
			if st == nil || st.Platform == nil {
				return nil
			}
			ref, ok := st.Platform.Trainings[id]
			if !ok {
				return nil
			}
			name := ref.Repo + "\x00" + ref.Ref
			if ref.Path != "" { // a training at the repo root keeps the clone it had before folders
				name += "\x00" + ref.Path
			}
			sum := sha256.Sum256([]byte(name))
			key := hex.EncodeToString(sum[:8])
			repoMu.Lock()
			defer repoMu.Unlock()
			if repos[key] == nil {
				repos[key] = &gitsync.ContentRepo{URL: ref.Repo, Branch: ref.Ref, Root: ref.Path, Dir: filepath.Join(env("CRUCIBLE_DATA_DIR", "/data"), "edits", key),
					Name: env("CRUCIBLE_GIT_BOT_NAME", "Crucible"), Email: env("CRUCIBLE_GIT_BOT_EMAIL", "crucible@localhost")}
			}
			return repos[key]
		}}
	authoringSvc := &authoring.Service{DB: pool, Edits: editsSvc, Log: slog.Default()}

	docPages, err := docs.All()
	if err != nil { // a broken page is a build bug: the coverage test catches it first
		slog.Error("docs", "err", err)
		os.Exit(1)
	}

	srv := &http.Server{
		Addr: env("CRUCIBLE_ADDR", ":8080"),
		Handler: httpapi.NewRouter(httpapi.Deps{Auth: store, OIDC: oidcH, Sync: syncer, Learn: learnSvc, Labs: labSvc, Scoring: scoreSvc, Notify: notifySvc, Config: cfgSvc, Hub: hub,
			Journey: &journey.Service{DB: pool, Learn: learnSvc, Now: time.Now}, Edits: editsSvc, Authoring: authoringSvc, Docs: docs.New(docPages),
			Org: orgStore, OrgAPI: orgAPI,
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

// awsSetup gives the aws runner its cloud and returns the aws lab estimator and the price list fetcher. Real labs are
// priced from a read-only terraform plan and prices, refreshed weekly by the fetcher; they need the plan role and the
// billing export (billed spend), so a missing one refuses to start. A dry run never calls newCloud (the real SDK
// client) and never fetches prices: it prices labs from CRUCIBLE_DEV_LAB_USD_PER_HOUR (rates).
func awsSetup(ctx context.Context, mode string, ar *labs.AWSRunner, rates labs.Estimator, getenv func(string) string,
	newCloud func(context.Context, awscloud.Config) (awscloud.Cloud, error), prices func(context.Context) (pricing.Lookup, error)) (labs.Estimator, *pricing.Fetcher, error) {
	if mode == "dryrun" {
		fake := &awscloud.Fake{}
		ar.Cloud, ar.DryRun = fake, fake
		slog.Warn("CRUCIBLE_AWS_LABS=dryrun: aws labs run without AWS (terraform pods only unpack the module; a fake lab account stands in) " +
			"and are priced from CRUCIBLE_DEV_LAB_USD_PER_HOUR. Development only")
		return rates, nil, nil
	}
	if ar.StateBucket == "" || ar.StateRegion == "" {
		return nil, nil, errors.New("CRUCIBLE_AWS_LABS=1 needs CRUCIBLE_AWS_STATE_BUCKET and CRUCIBLE_AWS_STATE_REGION")
	}
	for _, k := range []string{"CRUCIBLE_AWS_PLAN_ROLE_ARN", "CRUCIBLE_AWS_EXPORT_BUCKET", "CRUCIBLE_AWS_EXPORT_NAME"} {
		if getenv(k) == "" {
			return nil, nil, fmt.Errorf("CRUCIBLE_AWS_LABS=1 needs %s (a deploy/aws/labs output)", k)
		}
	}
	prefix := getenv("CRUCIBLE_AWS_EXPORT_PREFIX")
	if prefix == "" {
		prefix = "cur"
	}
	c, err := newCloud(ctx, awscloud.Config{LabRoleARN: getenv("CRUCIBLE_AWS_LAB_ROLE_ARN"), OpsRoleARN: getenv("CRUCIBLE_AWS_OPS_ROLE_ARN"),
		PlanRoleARN: getenv("CRUCIBLE_AWS_PLAN_ROLE_ARN"), ExportBucket: getenv("CRUCIBLE_AWS_EXPORT_BUCKET"), ExportPrefix: prefix,
		ExportName: getenv("CRUCIBLE_AWS_EXPORT_NAME")})
	if err != nil {
		return nil, nil, fmt.Errorf("aws labs: %w", err)
	}
	ar.Cloud = c
	return &labs.TerraformEstimator{Plan: ar, Prices: prices}, &pricing.Fetcher{Base: "https://pricing.us-east-1.amazonaws.com"}, nil
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
// End (tagging and CloudTrail lookups are free); billed-spend ingestion stays at 6 h; list prices refresh weekly.
func periodicJobs() []jobs.Periodic {
	return []jobs.Periodic{{Every: 15 * time.Second, Args: labs.SweepArgs{}}, {Every: 5 * time.Minute, Args: labs.BudgetArgs{}},
		{Every: time.Hour, Args: labs.ReapArgs{}}, {Every: 6 * time.Hour, Args: labs.CostArgs{}},
		{Every: 7 * 24 * time.Hour, Args: labs.PriceArgs{}}}
}
