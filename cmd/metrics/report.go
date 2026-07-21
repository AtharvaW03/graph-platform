package main

import (
	"context"
	"fmt"
	"io"
	"time"

	driver "github.com/neo4j/neo4j-go-driver/v5/neo4j"
)

func nowRFC3339() string { return time.Now().UTC().Format(time.RFC3339) }

// readAll runs a read query and returns every record as a map.
func readAll(ctx context.Context, sess driver.SessionWithContext, cypher string, params map[string]any) ([]map[string]any, error) {
	out, err := sess.ExecuteRead(ctx, func(tx driver.ManagedTransaction) (any, error) {
		res, err := tx.Run(ctx, cypher, params)
		if err != nil {
			return nil, err
		}
		recs, err := res.Collect(ctx)
		if err != nil {
			return nil, err
		}
		rows := make([]map[string]any, 0, len(recs))
		for _, r := range recs {
			rows = append(rows, r.AsMap())
		}
		return rows, nil
	})
	if err != nil {
		return nil, err
	}
	return out.([]map[string]any), nil
}

// readInt runs a query expected to return one row with an int column.
func readInt(ctx context.Context, sess driver.SessionWithContext, cypher, col string) (int, error) {
	rows, err := readAll(ctx, sess, cypher, nil)
	if err != nil {
		return 0, err
	}
	if len(rows) == 0 {
		return 0, nil
	}
	return asInt(rows[0][col]), nil
}

func asString(v any) string {
	s, _ := v.(string)
	return s
}

func asInt(v any) int {
	switch n := v.(type) {
	case int64:
		return int(n)
	case int:
		return n
	case float64:
		return int(n)
	default:
		return 0
	}
}

// printReport renders the human-readable text report.
func printReport(w io.Writer, r *Report) {
	fmt.Fprintf(w, "A1 Knowledge Graph - data quality report\n")
	fmt.Fprintf(w, "generated: %s   repositories indexed: %d\n\n", r.GeneratedAt, r.Repositories)

	if r.Repositories == 0 {
		fmt.Fprintf(w, "The graph is empty - nothing has been indexed yet. Run the indexer first.\n")
		return
	}

	// --- Relationship confidence ---
	fmt.Fprintf(w, "RELATIONSHIP CONFIDENCE  (excludes HAS_ENTITY, the platform ownership edge)\n")
	rc := r.Relationships
	fmt.Fprintf(w, "  headline: %.1f%% of %s relationships are EXTRACTED (explicit in source; near-certain)\n",
		rc.ExtractedShare*100, humanInt(rc.Total))
	for _, tier := range confidenceTiers {
		n := rc.ByTier[tier]
		if n == 0 && tier == "UNLABELED" {
			continue
		}
		fmt.Fprintf(w, "    %-10s %10s  %5.1f%%   %s\n", tier, humanInt(n), share(n, rc.Total)*100, tierGloss(tier))
	}
	fmt.Fprintf(w, "\n")

	// --- By relation type ---
	fmt.Fprintf(w, "BY RELATIONSHIP TYPE\n")
	fmt.Fprintf(w, "  %-20s %10s  %10s  %9s  %9s\n", "type", "total", "extracted", "inferred", "ambiguous")
	for _, t := range r.ByRelationType {
		fmt.Fprintf(w, "  %-20s %10s  %9.1f%%  %8.1f%%  %8.1f%%\n",
			t.Type, humanInt(t.Total),
			share(t.ByTier["EXTRACTED"], t.Total)*100,
			share(t.ByTier["INFERRED"], t.Total)*100,
			share(t.ByTier["AMBIGUOUS"], t.Total)*100,
		)
	}
	fmt.Fprintf(w, "\n")

	// --- Routes ---
	fmt.Fprintf(w, "HTTP ROUTE DOCUMENTATION\n")
	rt := r.Routes
	if rt.Total == 0 {
		fmt.Fprintf(w, "  no HTTP routes indexed\n\n")
	} else {
		fmt.Fprintf(w, "  %d routes total\n", rt.Total)
		fmt.Fprintf(w, "    %.1f%% spec-verified (documented against an OpenAPI/Swagger spec): %d\n", rt.DocumentedShare*100, rt.Documented)
		fmt.Fprintf(w, "    %.1f%% business surface vs %d infra (health/metrics/docs)\n", rt.BusinessShare*100, rt.Infra)
		fmt.Fprintf(w, "\n")
	}

	// --- Per repo ---
	fmt.Fprintf(w, "PER REPOSITORY\n")
	fmt.Fprintf(w, "  %-32s %10s %10s  %9s\n", "repo", "entities", "edges", "extracted")
	for _, rp := range r.PerRepoExtracted {
		ex := "-"
		if rp.Edges > 0 {
			ex = fmt.Sprintf("%.1f%%", rp.ExtractedShare*100)
		}
		fmt.Fprintf(w, "  %-32s %10s %10s  %9s\n", truncate(rp.Repo, 32), humanInt(rp.Entities), humanInt(rp.Edges), ex)
	}
	fmt.Fprintf(w, "\n")

	fmt.Fprintf(w, "Note: EXTRACTED = explicit in source (near-certain); INFERRED = heuristic (a lead,\n")
	fmt.Fprintf(w, "not a proof). This is static analysis, and measures the confidence of what was\n")
	fmt.Fprintf(w, "extracted - not precision/recall against hand-checked ground truth.\n")
}

func tierGloss(tier string) string {
	switch tier {
	case "EXTRACTED":
		return "explicit in source (import, resolved call)"
	case "INFERRED":
		return "heuristic / regex reasoning"
	case "AMBIGUOUS":
		return "uncertain"
	default:
		return "no confidence recorded"
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}

// humanInt groups thousands with commas for readability (e.g. 674123 -> 674,123).
func humanInt(n int) string {
	s := fmt.Sprintf("%d", n)
	neg := ""
	if n < 0 {
		neg, s = "-", s[1:]
	}
	var out []byte
	for i, c := range []byte(s) {
		if i > 0 && (len(s)-i)%3 == 0 {
			out = append(out, ',')
		}
		out = append(out, c)
	}
	return neg + string(out)
}
