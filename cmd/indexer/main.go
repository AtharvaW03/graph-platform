package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"a1-knowledge-graph/internal/extract"
	"a1-knowledge-graph/internal/extract/deps"
	"a1-knowledge-graph/internal/extract/glue"
	"a1-knowledge-graph/internal/extract/httpapi"
	"a1-knowledge-graph/internal/extract/kafka"
	"a1-knowledge-graph/internal/extract/mssql"
	"a1-knowledge-graph/internal/githubapp"
	"a1-knowledge-graph/internal/httpmw"
	"a1-knowledge-graph/internal/index"
	"a1-knowledge-graph/internal/neo4j"
)

// webhookDebounce is how long a webhook-triggered cycle waits after the first
// delivery before starting, so a burst (a release train pushing many repos
// within seconds) coalesces into one cycle instead of N.
const webhookDebounce = 10 * time.Second

func main() {
	configPath := flag.String("config", "config/repos.yaml", "path to indexer YAML config")
	workDir := flag.String("workdir", "workdir", "directory for clones, graphify outputs, and state.json")
	repos := flag.String("repo", "", "comma-separated repository names to index (default: all)")
	all := flag.Bool("all", false, "explicit all-repositories mode; mutually exclusive with --repo")
	force := flag.Bool("force", false, "re-index even if HEAD matches the previously-indexed commit")
	interval := flag.Duration("interval", 0, "if > 0, run continuously every interval (e.g. 15m); otherwise one-shot")
	webhookAddr := flag.String("webhook-addr", "", "if set (e.g. 0.0.0.0:8091), serve a GitHub push-webhook endpoint at /webhook/github and re-index repos as deliveries arrive; --interval then becomes the reconciliation sweep period and is still required (GITHUB_WEBHOOK_SECRET must be set)")
	leaseTTL := flag.Duration("lease-ttl", 15*time.Minute, "writer lease TTL; a background heartbeat renews it every ttl/4 and gives up after 3 consecutive failures, strictly before expiry - a crashed indexer's lease self-expires after this")
	stealLease := flag.Bool("steal-lease", false, "take the writer lease unconditionally at startup; operator recovery for a stuck lease")
	flag.Parse()

	logger := log.New(os.Stderr, "", log.LstdFlags|log.Lmsgprefix)

	if flag.NArg() > 0 {
		// Repository names must be passed via --repo; a silently ignored
		// positional argument would widen the run's scope to all repos.
		logger.Fatalf("unexpected argument(s) %q: repository names go in --repo (e.g. --repo %s)", flag.Args(), flag.Arg(0))
	}
	if *all && strings.TrimSpace(*repos) != "" {
		logger.Fatal("--all and --repo are mutually exclusive")
	}
	if *leaseTTL < time.Minute {
		// time.NewTicker panics on ttl/4 <= 0.
		logger.Fatalf("--lease-ttl must be at least 1m, got %s", *leaseTTL)
	}
	webhookSecret := os.Getenv("GITHUB_WEBHOOK_SECRET")
	if *webhookAddr != "" {
		if *interval <= 0 {
			// GitHub does not retry failed webhook deliveries; the periodic
			// sweep is what bounds staleness, so --interval is required.
			logger.Fatal("--webhook-addr requires --interval: webhook deliveries are best-effort (GitHub does not retry failures), so a periodic reconciliation sweep is what bounds staleness. 30m is a reasonable value.")
		}
		if strings.TrimSpace(*repos) != "" {
			logger.Fatal("--webhook-addr and --repo are mutually exclusive: webhook mode serves the full configured manifest")
		}
		if webhookSecret == "" {
			logger.Fatal("--webhook-addr requires GITHUB_WEBHOOK_SECRET: an unauthenticated webhook endpoint would let anyone who can reach it trigger indexing work")
		}
	}

	cfg, err := index.LoadConfig(*configPath)
	if err != nil {
		logger.Fatal(err)
	}

	absWorkDir, err := filepath.Abs(*workDir)
	if err != nil {
		logger.Fatalf("resolve workdir: %v", err)
	}
	if err := os.MkdirAll(absWorkDir, 0o755); err != nil {
		logger.Fatalf("create workdir: %v", err)
	}

	// Acquire local resources first so config/disk errors surface before
	// the Neo4j connect attempt.
	lock, err := index.LockWorkDir(absWorkDir)
	if err != nil {
		logger.Fatal(err)
	}
	if lock != nil {
		defer lock.Close()
	}

	store, err := index.LoadJSONStateStore(filepath.Join(absWorkDir, "state.json"))
	if err != nil {
		logger.Fatalf("load state: %v", err)
	}

	password := os.Getenv("NEO4J_PASSWORD")
	if password == "" {
		logger.Fatal("NEO4J_PASSWORD not set")
	}
	uri := envOr("NEO4J_URI", "neo4j://127.0.0.1:7687")
	user := envOr("NEO4J_USER", "neo4j")

	client, err := neo4j.New(uri, user, password)
	if err != nil {
		logger.Fatalf("neo4j connect: %v", err)
	}
	defer client.Close()
	logger.Printf("connected to neo4j (%s)", uri)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Must run before AcquireLease: on a brand-new database the
	// IndexerLease uniqueness constraint does not exist yet, and two
	// processes racing their first MERGE could each create a lease row.
	if err := client.EnsureConstraints(ctx); err != nil {
		logger.Fatalf("ensure constraints: %v", err)
	}

	// Verify the graphify version before any repo is processed; a version
	// mismatch stops the run.
	if err := index.CheckGraphifyVersion(ctx, cfg.Graphify, logger); err != nil {
		logger.Fatal(err)
	}

	// Git auth must be in place before the first clone.
	ghClient, err := setupGitAuth(ctx, logger)
	if err != nil {
		logger.Fatal(err)
	}

	// The manifest source: the static config list by default; with
	// discovery enabled, the set of repos the GitHub App installation
	// covers, refreshed at most every discovery.ttl and merged with any
	// static entries (static wins by name).
	var source index.JobSource = index.NewConfigJobSource(cfg)
	if cfg.Discovery.Enabled {
		if ghClient == nil {
			logger.Fatal("discovery.enabled requires GitHub App auth: set GITHUB_APP_ID, GITHUB_APP_INSTALLATION_ID, and GITHUB_APP_PRIVATE_KEY_PATH")
		}
		fetch := func(fctx context.Context) ([]index.DiscoveredRepo, error) {
			installed, err := ghClient.ListInstallationRepos(fctx)
			if err != nil {
				return nil, err
			}
			out := make([]index.DiscoveredRepo, 0, len(installed))
			for _, r := range installed {
				out = append(out, index.DiscoveredRepo{
					Name:     r.Name,
					URL:      r.CloneURL,
					Branch:   r.DefaultBranch,
					Archived: r.Archived,
				})
			}
			return out, nil
		}
		ds, err := index.NewDiscoveryJobSource(fetch, cfg.Repositories, cfg.Discovery.TTL, logger)
		if err != nil {
			logger.Fatal(err)
		}
		logger.Printf("repository discovery: GitHub App installation listing (refresh <= every %s, %d static entries)", cfg.Discovery.TTL, len(cfg.Repositories))
		source = ds
	}

	owner := neo4j.LeaseOwner()
	if *stealLease {
		logger.Printf("WARNING: --steal-lease set, taking writer lease unconditionally (owner=%s)", owner)
		if err := client.StealLease(ctx, owner, *leaseTTL); err != nil {
			logger.Fatalf("steal lease: %v", err)
		}
	} else if err := client.AcquireLease(ctx, owner, *leaseTTL); err != nil {
		var held *neo4j.ErrLeaseHeld
		if errors.As(err, &held) {
			logger.Fatalf("writer lease held by %q until %s; another indexer is running against this database. Use --steal-lease to recover a stuck lease.",
				held.Owner, held.Expires.Format(time.RFC3339))
		}
		logger.Fatalf("acquire writer lease: %v", err)
	}
	logger.Printf("writer lease acquired (owner=%s, ttl=%s)", owner, *leaseTTL)
	// Every exit path after this point must release the lease.
	// logger.Fatal skips defers, so the fatal paths below call releaseLease
	// explicitly.
	releaseLease := func() {
		releaseCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := client.ReleaseLease(releaseCtx, owner); err != nil {
			logger.Printf("WARNING: release lease failed: %v", err)
		}
	}
	defer releaseLease()

	// runCtx is what the orchestrator runs under, separate from ctx (which
	// only the OS signal cancels) so the lease heartbeat can cancel
	// in-flight work independently of a shutdown.
	runCtx, cancelRun := context.WithCancel(ctx)
	defer cancelRun()

	// The orchestrator renews only between repos; this heartbeat renews on
	// a fixed tick so a single long repo cannot outlive the TTL. Both paths
	// call the same owner-guarded RenewLease.
	heartbeat := &index.LeaseHeartbeat{
		Renew:    func(hbCtx context.Context) error { return client.RenewLease(hbCtx, owner, *leaseTTL) },
		Interval: *leaseTTL / 4,
		Log:      logger,
		IsLost:   func(err error) bool { return errors.Is(err, neo4j.ErrLeaseLost) },
		OnFatal: func(err error) {
			logger.Printf("FATAL: writer lease heartbeat gave up (%v); canceling all in-flight work", err)
			cancelRun()
		},
	}
	go heartbeat.Run(ctx)

	orch := &index.Orchestrator{
		Source:                      source,
		Syncer:                      index.NewGitSyncer(cfg.Git),
		Graphify:                    index.NewExecGraphifier(cfg.Graphify, os.Stderr),
		Importer:                    index.NewDefaultImportRunner(client),
		Store:                       store,
		WorkDir:                     absWorkDir,
		Log:                         logger,
		HealthChecker:               client,
		Retirer:                     client,
		SyncStamper:                 client,
		Lease:                       clientLeaseRenewer{client: client, owner: owner, ttl: *leaseTTL},
		Extractors:                  buildExtractorRunner(cfg, logger),
		AllowPartialExtractorErrors: cfg.Extractors.AllowPartialEnabled(),
	}

	opts := index.Options{
		Names: splitCSV(*repos),
		Force: *force,
	}

	if *interval > 0 {
		var (
			sched  index.Scheduler
			optsFn = func() index.Options { return opts }
			// Buffered so the server goroutine never blocks on a fatal error;
			// stays empty (and unread until after RunForever) in interval mode.
			webhookSrvErr = make(chan error, 1)
		)
		if *webhookAddr != "" {
			pending := index.NewPendingSet()
			wsched, err := index.NewWebhookScheduler(pending, *interval, webhookDebounce)
			if err != nil {
				logger.Fatal(err)
			}
			handler, err := index.NewGitHubWebhookHandler(source, webhookSecret, pending, logger)
			if err != nil {
				logger.Fatal(err)
			}
			srv := webhookServer(*webhookAddr, handler, source, store, pending, logger)
			go func() {
				// runCtx (not ctx): a lease-heartbeat give-up must also stop
				// accepting deliveries, not just the indexing loop.
				<-runCtx.Done()
				shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				_ = srv.Shutdown(shutdownCtx)
			}()
			go func() {
				logger.Printf("webhook listener on %s (reconciliation sweep every %s)", *webhookAddr, *interval)
				if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
					// Exit on listener failure rather than continue in
					// sweep-only mode.
					webhookSrvErr <- err
					cancelRun()
				}
			}()
			optsFn = func() index.Options {
				o := wsched.NextOptions()
				o.Force = *force
				if len(o.Names) > 0 {
					logger.Printf("webhook-triggered cycle: %s", strings.Join(o.Names, ", "))
				} else {
					logger.Printf("reconciliation sweep (full manifest)")
				}
				return o
			}
			sched = wsched
		} else {
			isched, err := index.NewIntervalScheduler(*interval)
			if err != nil {
				logger.Fatal(err)
			}
			logger.Printf("continuous mode: every %s", *interval)
			sched = isched
		}
		runErr := orch.RunForeverDynamic(runCtx, optsFn, sched)
		// A confirmed lease loss exits nonzero regardless of how RunForever
		// returned; the ctx.Err() check below distinguishes an operator
		// shutdown from other failures.
		if fatal := exitErr(0, heartbeat.FatalErr()); fatal != nil {
			releaseLease()
			logger.Fatal(fatal)
		}
		// Checked before runErr: a webhook-server failure cancels runCtx and
		// would otherwise surface as an opaque "context canceled".
		select {
		case err := <-webhookSrvErr:
			releaseLease()
			logger.Fatalf("webhook listener failed: %v", err)
		default:
		}
		if runErr != nil && ctx.Err() == nil {
			releaseLease()
			logger.Fatal(runErr)
		}
		return
	}

	summary, err := orch.RunOnce(runCtx, opts)
	if err != nil {
		releaseLease()
		logger.Fatal(err)
	}
	orch.LogSummary(summary)

	_, _, _, failed := summary.Counts()
	if fatal := exitErr(failed, heartbeat.FatalErr()); fatal != nil {
		releaseLease()
		logger.Fatal(fatal)
	}
}

// exitErr decides whether the process exits nonzero after a run. A
// confirmed lease-heartbeat loss always wins, even with zero failed repos.
// hbErr comes from LeaseHeartbeat.FatalErr().
func exitErr(failed int, hbErr error) error {
	if hbErr != nil {
		return fmt.Errorf("writer lease heartbeat failed: %w", hbErr)
	}
	if failed > 0 {
		return fmt.Errorf("%d repositories failed", failed)
	}
	return nil
}

// webhookServer assembles the webhook-mode HTTP server: the HMAC-verified
// GitHub endpoint, an unauthenticated liveness probe, and a bearer-token
// /status snapshot. Timeouts mirror the other services' servers, with
// ReadTimeout sized for GitHub's up-to-25MB push payloads.
func webhookServer(addr string, webhook http.Handler, source index.JobSource, store index.StateStore, pending *index.PendingSet, logger *log.Logger) *http.Server {
	mux := http.NewServeMux()
	mux.Handle("POST /webhook/github", webhook)
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	statusToken := os.Getenv("INDEXER_STATUS_TOKEN")
	if statusToken == "" {
		logger.Printf("WARNING: INDEXER_STATUS_TOKEN not set - /status (repo names, commit SHAs, error details) is served unauthenticated")
	}
	mux.Handle("GET /status", httpmw.WithAuth(index.NewStatusHandler(source, store, pending), statusToken))
	return &http.Server{
		Addr:              addr,
		Handler:           httpmw.WithRequestLog(mux, logger),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
}

func splitCSV(s string) []string {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := parts[:0]
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// clientLeaseRenewer adapts *neo4j.Client.RenewLease to index.LeaseRenewer,
// capturing the fixed owner/ttl for this process's lifetime.
type clientLeaseRenewer struct {
	client *neo4j.Client
	owner  string
	ttl    time.Duration
}

func (r clientLeaseRenewer) Renew(ctx context.Context) error {
	return r.client.RenewLease(ctx, r.owner, r.ttl)
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// setupGitAuth configures how GitSyncer's git subprocess authenticates
// against private HTTPS remotes. When GITHUB_APP_ID,
// GITHUB_APP_INSTALLATION_ID, and GITHUB_APP_PRIVATE_KEY_PATH are all set, it
// mints a GitHub App installation token and writes it to the git credential
// store, then starts a background goroutine that re-mints and re-writes it
// before each hourly token expires, and returns the App client for other
// GitHub API needs (repository discovery). With any of the three unset, it
// returns nil: git auth stays exactly as deploy/indexer-entrypoint.sh
// configured it (GIT_TOKEN, or none for public repos).
func setupGitAuth(ctx context.Context, logger *log.Logger) (*githubapp.Client, error) {
	appID := os.Getenv("GITHUB_APP_ID")
	installationID := os.Getenv("GITHUB_APP_INSTALLATION_ID")
	keyPath := os.Getenv("GITHUB_APP_PRIVATE_KEY_PATH")
	if appID == "" && installationID == "" && keyPath == "" {
		logger.Printf("git auth: GIT_TOKEN (personal access token), if set")
		return nil, nil
	}
	if appID == "" || installationID == "" || keyPath == "" {
		return nil, fmt.Errorf("GITHUB_APP_ID, GITHUB_APP_INSTALLATION_ID, and GITHUB_APP_PRIVATE_KEY_PATH must all be set together to enable GitHub App auth")
	}

	pemBytes, err := os.ReadFile(keyPath)
	if err != nil {
		return nil, fmt.Errorf("read github app private key: %w", err)
	}
	ghClient, err := githubapp.New(appID, installationID, pemBytes)
	if err != nil {
		return nil, fmt.Errorf("build github app client: %w", err)
	}
	logger.Printf("git auth: GitHub App (installation %s)", installationID)
	if os.Getenv("GIT_TOKEN") != "" {
		// Both auth modes write the same credential file; the App token
		// wins.
		logger.Printf("WARNING: GIT_TOKEN is set but ignored (GitHub App auth takes precedence)")
	}

	if out, err := exec.CommandContext(ctx, "git", "config", "--global", "credential.helper", "store").CombinedOutput(); err != nil {
		return nil, fmt.Errorf("git config credential.helper: %w: %s", err, out)
	}
	if err := writeGitCredential(ctx, ghClient); err != nil {
		return nil, fmt.Errorf("write initial github app credential: %w", err)
	}

	go func() {
		// Installation tokens last 1h. Refresh forces a fresh mint so the
		// credential store always holds a token with at least 10 minutes
		// left.
		ticker := time.NewTicker(50 * time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := writeGitCredential(ctx, ghClient); err != nil {
					logger.Printf("WARNING: github app token refresh failed: %v", err)
				}
			}
		}
	}()
	return ghClient, nil
}

// writeGitCredential mints a fresh installation token and writes it to the
// git credential store in the "https://x-access-token:TOKEN@github.com"
// format. The write is atomic (temp file + rename) with mode 0600, and the
// token is never logged.
func writeGitCredential(ctx context.Context, ghClient *githubapp.Client) error {
	token, err := ghClient.Refresh(ctx)
	if err != nil {
		return fmt.Errorf("mint installation token: %w", err)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("resolve home directory: %w", err)
	}
	target := filepath.Join(home, ".git-credentials")
	tmp, err := os.CreateTemp(home, ".git-credentials.tmp-*")
	if err != nil {
		return fmt.Errorf("create temp credential file: %w", err)
	}
	defer os.Remove(tmp.Name())
	line := fmt.Sprintf("https://x-access-token:%s@github.com\n", token)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return fmt.Errorf("chmod temp credential file: %w", err)
	}
	if _, err := tmp.WriteString(line); err != nil {
		tmp.Close()
		return fmt.Errorf("write temp credential file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp credential file: %w", err)
	}
	if err := os.Rename(tmp.Name(), target); err != nil {
		return fmt.Errorf("replace git credentials: %w", err)
	}
	return nil
}

// buildExtractorRunner constructs the extract.Runner from the platform's
// domain extractors (deps, http_api, kafka, mssql, glue), each toggleable via
// cfg.Extractors. Returns nil if all are disabled.
func buildExtractorRunner(cfg *index.Config, logger *log.Logger) *extract.Runner {
	var exs []extract.Extractor
	if cfg.Extractors.DepsEnabled() {
		exs = append(exs, deps.New(cfg.Org.Prefixes))
	}
	if cfg.Extractors.HTTPAPIEnabled() {
		exs = append(exs, httpapi.New())
	}
	if cfg.Extractors.KafkaEnabled() {
		exs = append(exs, kafka.New())
	}
	if cfg.Extractors.MSSQLEnabled() {
		exs = append(exs, mssql.New())
	}
	if cfg.Extractors.GlueEnabled() {
		exs = append(exs, glue.New())
	}
	if len(exs) == 0 {
		return nil
	}
	return &extract.Runner{
		Extractors:  exs,
		Log:         logger,
		MaxParallel: cfg.Extractors.MaxParallel,
	}
}
