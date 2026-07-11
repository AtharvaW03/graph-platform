package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"time"

	"graph-platform/internal/importer"
	"graph-platform/internal/index"
	"graph-platform/internal/neo4j"
)

func main() {
	repo := flag.String("repo", "", "repository name to scope the imported graph (required)")
	graphPath := flag.String("graph", "", "path to the graph.json to import (required)")
	commit := flag.String("commit", "", "source commit SHA to stamp on imported nodes/edges; non-empty enables sweep of stale data from prior commits")
	leaseTTL := flag.Duration("lease-ttl", 15*time.Minute, "writer lease TTL for this run; a background heartbeat renews it every ttl/3, so this just needs headroom over a missed heartbeat or two")
	flag.Parse()

	if *repo == "" || *graphPath == "" {
		flag.Usage()
		log.Fatal("both --repo and --graph are required")
	}
	if *leaseTTL < time.Minute {
		log.Fatalf("--lease-ttl must be at least 1m, got %s", *leaseTTL)
	}

	password := os.Getenv("NEO4J_PASSWORD")
	if password == "" {
		log.Fatal("NEO4J_PASSWORD not set")
	}

	client, err := neo4j.New("neo4j://127.0.0.1:7687", "neo4j", password)
	if err != nil {
		log.Fatal(err)
	}
	defer client.Close()
	fmt.Println("Connected to Neo4j")

	ctx := context.Background()

	// Idempotent, and must run before AcquireLease: on a brand-new database
	// the IndexerLease uniqueness constraint doesn't exist yet, so two
	// processes racing their very first MERGE on the lease row could each
	// create one. The in-pipeline call inside importer.Run later is harmless.
	if err := client.EnsureConstraints(ctx); err != nil {
		log.Fatalf("ensure constraints: %v", err)
	}

	// This CLI writes with the same credentials as the indexer, so it must
	// take the same lease - otherwise it's a side door around the dual-writer
	// protection. No --steal-lease here: recovering a stuck lease is the
	// indexer's job, not a one-off import's.
	owner := neo4j.LeaseOwner()
	if err := client.AcquireLease(ctx, owner, *leaseTTL); err != nil {
		var held *neo4j.ErrLeaseHeld
		if errors.As(err, &held) {
			log.Fatalf("writer lease held by %q until %s; stop the indexer first, then re-run.",
				held.Owner, held.Expires.Format(time.RFC3339))
		}
		log.Fatalf("acquire writer lease: %v", err)
	}
	defer func() {
		releaseCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := client.ReleaseLease(releaseCtx, owner); err != nil {
			log.Printf("WARNING: release lease failed: %v", err)
		}
	}()

	// runCtx is what the import actually runs under, separate from ctx so the
	// heartbeat can cut it off the moment it gives up on the lease. A single
	// graph.json import is usually well inside the TTL, but nothing stops a
	// very large one from running long - same reasoning as the indexer.
	runCtx, cancelRun := context.WithCancel(ctx)
	defer cancelRun()

	heartbeat := &index.LeaseHeartbeat{
		Renew:    func(hbCtx context.Context) error { return client.RenewLease(hbCtx, owner, *leaseTTL) },
		Interval: *leaseTTL / 3,
		Log:      log.Default(),
		IsLost:   func(err error) bool { return errors.Is(err, neo4j.ErrLeaseLost) },
		OnFatal: func(err error) {
			log.Printf("FATAL: writer lease heartbeat gave up (%v); canceling the import", err)
			cancelRun()
		},
	}
	go heartbeat.Run(ctx)

	progress := func(stage string) {
		switch stage {
		case importer.StageLoad:
			// graph.json is parsed inside importer.Run; print after success
		case importer.StageConstraints:
			fmt.Println("Ensuring constraints/indexes")
		case importer.StageNodes:
			fmt.Println("Importing nodes")
		case importer.StageLinks:
			fmt.Println("Importing links")
		case importer.StageSweep:
			fmt.Println("Sweeping stale data")
		case importer.StageVerifySweep:
			fmt.Println("Verifying sweep left nothing stale")
		case importer.StageVerify:
			fmt.Println("Verifying Neo4j count")
		}
	}

	summary, err := importer.Run(runCtx, client, importer.Options{
		Repo:      *repo,
		Commit:    *commit,
		GraphPath: *graphPath,
		// This CLI is the manual/repair path - always do a full property
		// rewrite so its output doesn't depend on content-hash state left by
		// a previous run.
		RewriteAll: true,
		Progress:   progress,
	})
	if err != nil {
		log.Fatal(err)
	}
	// A canceled runCtx from a plain ctx.Err() check wouldn't say why; check
	// the heartbeat's own recorded error instead, same reasoning as the
	// indexer. A lease lost partway through must fail the run even though
	// importer.Run above may have returned without an error before the loss
	// was noticed.
	if hbErr := heartbeat.FatalErr(); hbErr != nil {
		log.Fatalf("writer lease heartbeat failed: %v", hbErr)
	}

	fmt.Printf("Loaded graph: %d nodes, %d links\n", summary.NodesTotal, summary.LinksTotal)
	fmt.Printf("Imported %d nodes\n", summary.NodesTotal)
	fmt.Printf("Imported %d links\n", summary.LinksImported)

	fmt.Println("\n--- Import Summary ---")
	fmt.Println("Nodes by label:")
	for _, l := range summary.SortedLabels() {
		fmt.Printf("  %-12s %d\n", l, summary.LabelCounts[l])
	}
	fmt.Println("Links by relation:")
	for _, r := range summary.SortedRelations() {
		fmt.Printf("  %-16s %d\n", r, summary.RelationCounts[r])
	}
	fmt.Printf("Skipped (unknown relation): %d\n", summary.SkippedUnknown)
	fmt.Printf("Skipped (dangling edge):    %d\n", summary.SkippedDangling)
	if summary.SkippedHyperedges > 0 {
		fmt.Printf("Skipped (hyperedges):       %d\n", summary.SkippedHyperedges)
	}
	if summary.Commit != "" {
		fmt.Printf("Swept (stale nodes):        %d\n", summary.NodesSwept)
		fmt.Printf("Swept (stale relations):    %d\n", summary.EdgesSwept)
	}
	fmt.Printf("Neo4j :Entity count (repo): %d\n", summary.NodesInGraph)
	if summary.NodesMismatch() {
		fmt.Printf("\nWARNING: input %d nodes but Neo4j holds %d for repo %q (delta %d).\n",
			summary.NodesTotal, summary.NodesInGraph, summary.Repo,
			summary.NodesTotal-summary.NodesInGraph)
		fmt.Println("This indicates silent data loss during import (e.g. node_key collisions). Investigate.")
	}
	if summary.HasSweepResidue() {
		fmt.Printf("\nERROR: sweep left %d stale nodes and %d stale relationships behind for repo %q.\n",
			summary.SweepResidueNodes, summary.SweepResidueRels, summary.Repo)
		fmt.Println("SweepStale should have removed these. Investigate the sweep logic or a concurrent writer.")
	}
}
