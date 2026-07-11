package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"graph-platform/internal/extract"
	"graph-platform/internal/extract/deps"
	"graph-platform/internal/extract/glue"
	"graph-platform/internal/extract/httpapi"
	"graph-platform/internal/extract/kafka"
	"graph-platform/internal/extract/mssql"
	"graph-platform/internal/index"
	"graph-platform/internal/neo4j"
)

func main() {
	configPath := flag.String("config", "config/repos.yaml", "path to indexer YAML config")
	workDir := flag.String("workdir", "workdir", "directory for clones, graphify outputs, and state.json")
	repos := flag.String("repo", "", "comma-separated repository names to index (default: all)")
	all := flag.Bool("all", false, "explicit all-repositories mode; mutually exclusive with --repo")
	force := flag.Bool("force", false, "re-index even if HEAD matches the previously-indexed commit")
	interval := flag.Duration("interval", 0, "if > 0, run continuously every interval (e.g. 15m); otherwise one-shot")
	leaseTTL := flag.Duration("lease-ttl", 15*time.Minute, "writer lease TTL; a background heartbeat renews it every ttl/3, so the TTL just needs headroom over a missed heartbeat or two, not the whole run - a crashed indexer's lease self-expires after this")
	stealLease := flag.Bool("steal-lease", false, "take the writer lease unconditionally at startup; operator recovery for a stuck lease")
	flag.Parse()

	logger := log.New(os.Stderr, "", log.LstdFlags|log.Lmsgprefix)

	if *all && strings.TrimSpace(*repos) != "" {
		logger.Fatal("--all and --repo are mutually exclusive")
	}
	if *leaseTTL < time.Minute {
		// time.NewTicker panics on ttl/3 <= 0, and anything sub-minute is
		// operationally pointless (renewal jitter alone could exceed it).
		logger.Fatalf("--lease-ttl must be at least 1m, got %s", *leaseTTL)
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

	// Acquire local resources first so config/disk errors surface before the
	// Neo4j connect attempt, the only network dependency.
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

	// Idempotent, and must run before AcquireLease: on a brand-new database
	// the IndexerLease uniqueness constraint doesn't exist yet, so without
	// this, two processes racing their very first MERGE on the lease row
	// could each create one. The importer's own in-pipeline call later is
	// harmless - this one is what makes the lease itself safe.
	if err := client.EnsureConstraints(ctx); err != nil {
		logger.Fatalf("ensure constraints: %v", err)
	}

	// Runs once, before any repo is touched: a graphify upgraded outside the
	// pinned Docker image should stop the run, not silently produce a
	// differently-shaped graph partway through a batch.
	if err := index.CheckGraphifyVersion(ctx, cfg.Graphify, logger); err != nil {
		logger.Fatal(err)
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
	defer func() {
		releaseCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := client.ReleaseLease(releaseCtx, owner); err != nil {
			logger.Printf("WARNING: release lease failed: %v", err)
		}
	}()

	// runCtx is what the orchestrator actually runs under. It's separate from
	// ctx (which only the OS signal cancels) so the lease heartbeat can cut
	// off in-flight work the moment it gives up on the lease, without that
	// also looking like an operator-initiated shutdown.
	runCtx, cancelRun := context.WithCancel(ctx)
	defer cancelRun()

	// The orchestrator only renews between repos (see Orchestrator.Lease in
	// internal/index/orchestrator.go), which leaves a gap: one repo whose
	// graphify run alone outlives the TTL. This heartbeat renews on a fixed
	// tick independent of repo boundaries to close that gap. Both renewal
	// paths call the same owner-guarded RenewLease, so they never conflict.
	heartbeat := &index.LeaseHeartbeat{
		Renew:    func(hbCtx context.Context) error { return client.RenewLease(hbCtx, owner, *leaseTTL) },
		Interval: *leaseTTL / 3,
		Log:      logger,
		IsLost:   func(err error) bool { return errors.Is(err, neo4j.ErrLeaseLost) },
		OnFatal: func(err error) {
			logger.Printf("FATAL: writer lease heartbeat gave up (%v); canceling all in-flight work", err)
			cancelRun()
		},
	}
	go heartbeat.Run(ctx)

	orch := &index.Orchestrator{
		Source:                      index.NewConfigJobSource(cfg),
		Syncer:                      index.NewGitSyncer(cfg.Git),
		Graphify:                    index.NewExecGraphifier(cfg.Graphify, os.Stderr),
		Importer:                    index.NewDefaultImportRunner(client),
		Store:                       store,
		WorkDir:                     absWorkDir,
		Log:                         logger,
		HealthChecker:               client,
		Lease:                       clientLeaseRenewer{client: client, owner: owner, ttl: *leaseTTL},
		Extractors:                  buildExtractorRunner(cfg, logger),
		AllowPartialExtractorErrors: cfg.Extractors.AllowPartialEnabled(),
	}

	opts := index.Options{
		Names: splitCSV(*repos),
		Force: *force,
	}

	if *interval > 0 {
		sched, err := index.NewIntervalScheduler(*interval)
		if err != nil {
			logger.Fatal(err)
		}
		logger.Printf("continuous mode: every %s", *interval)
		runErr := orch.RunForever(runCtx, opts, sched)
		// Checked against the recorded heartbeat error, not ctx state: a
		// confirmed lease loss must exit nonzero regardless of how RunForever
		// itself returned. The ctx.Err() check below is a separate, older
		// mechanism (distinguishing an operator shutdown from any other
		// RunForever failure) and stays for that.
		if fatal := exitErr(0, heartbeat.FatalErr()); fatal != nil {
			logger.Fatal(fatal)
		}
		if runErr != nil && ctx.Err() == nil {
			logger.Fatal(runErr)
		}
		return
	}

	summary, err := orch.RunOnce(runCtx, opts)
	if err != nil {
		logger.Fatal(err)
	}
	orch.LogSummary(summary)

	_, _, _, failed := summary.Counts()
	if fatal := exitErr(failed, heartbeat.FatalErr()); fatal != nil {
		logger.Fatal(fatal)
	}
}

// exitErr decides whether the process should exit nonzero after a run
// completes. A confirmed lease-heartbeat loss always wins, even with zero
// failed repos: RunOnce's own return only covers "stopped before finishing"
// (ctx.Err() breaks its loop and returns a nil error), not "finished, but a
// stale write already happened under a lease we no longer held." hbErr should
// come from LeaseHeartbeat.FatalErr(), which is only set on a genuine
// give-up - never inferred from context-cancellation state.
func exitErr(failed int, hbErr error) error {
	if hbErr != nil {
		return fmt.Errorf("writer lease heartbeat failed: %w", hbErr)
	}
	if failed > 0 {
		return fmt.Errorf("%d repositories failed", failed)
	}
	return nil
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
