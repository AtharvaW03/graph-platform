// Command metrics reports data-quality signals over the currently-indexed
// graph, so a real, dated number can back any accuracy claim instead of a
// guess. It is read-only (no writer lease) and connects with the same Neo4j
// env vars as query-service:
//
//	NEO4J_PASSWORD=... go run ./cmd/metrics            # human-readable report
//	NEO4J_PASSWORD=... go run ./cmd/metrics --json     # machine-readable
//
// What it measures, and what each number does and does not mean:
//
//   - Relationship confidence. Every extracted edge is tagged EXTRACTED
//     (explicit in source - an import, a resolved call: near-certain),
//     INFERRED (heuristic/regex reasoning: a lead, not a proof), or
//     AMBIGUOUS. The headline "% EXTRACTED" is the honest one-line quality
//     signal. HAS_ENTITY (the platform's own repo-ownership edge, never an
//     extraction claim) is excluded so it can't inflate the number.
//   - HTTP route documentation. What share of the discovered HTTP surface is
//     verified against an OpenAPI/Swagger spec (documented=true) vs inferred
//     from code alone.
//
// This reflects static/heuristic analysis, not runtime verification, and it
// is a precision-of-labeling signal, not a measured precision/recall against
// hand-checked ground truth - that still needs a sampling exercise. It answers
// "how much of what we extracted is high-confidence", not "how much of what
// exists did we find".
package main

import (
	"context"
	"encoding/json"
	"flag"
	"log"
	"os"
	"sort"

	"a1-knowledge-graph/internal/neo4j"

	driver "github.com/neo4j/neo4j-go-driver/v5/neo4j"
)

// confidence tiers, in report order. Mirrors internal/extract.Confidence*.
var confidenceTiers = []string{"EXTRACTED", "INFERRED", "AMBIGUOUS", "UNLABELED"}

// Report is the full machine-readable output (--json).
type Report struct {
	GeneratedAt      string          `json:"generated_at"`
	Repositories     int             `json:"repositories"`
	Relationships    ConfidenceBlock `json:"relationships"`
	ByRelationType   []RelTypeStat   `json:"by_relation_type"`
	Routes           RouteStats      `json:"routes"`
	PerRepoExtracted []RepoStat      `json:"per_repo"`
}

// ConfidenceBlock is a total plus its per-tier split.
type ConfidenceBlock struct {
	Total          int            `json:"total"`
	ByTier         map[string]int `json:"by_tier"`
	ExtractedShare float64        `json:"extracted_share"` // 0..1 over Total
}

type RelTypeStat struct {
	Type           string         `json:"type"`
	Total          int            `json:"total"`
	ByTier         map[string]int `json:"by_tier"`
	ExtractedShare float64        `json:"extracted_share"`
}

type RouteStats struct {
	Total           int     `json:"total"`
	Documented      int     `json:"documented"`     // reconciled with a spec
	FromSpecOnly    int     `json:"from_spec_only"` // source == openapi
	Business        int     `json:"business"`       // classification == business
	Infra           int     `json:"infra"`          // classification == infra
	DocumentedShare float64 `json:"documented_share"`
	BusinessShare   float64 `json:"business_share"`
}

type RepoStat struct {
	Repo           string  `json:"repo"`
	Entities       int     `json:"entities"`
	Edges          int     `json:"edges"`
	ExtractedShare float64 `json:"extracted_share"`
}

func main() {
	asJSON := flag.Bool("json", false, "emit the report as JSON instead of a text table")
	flag.Parse()

	password := os.Getenv("NEO4J_PASSWORD")
	if password == "" {
		log.Fatal("NEO4J_PASSWORD not set")
	}
	uri := envOr("NEO4J_URI", "neo4j://127.0.0.1:7687")
	user := envOr("NEO4J_USER", "neo4j")

	client, err := neo4j.New(uri, user, password)
	if err != nil {
		log.Fatalf("neo4j connect: %v", err)
	}
	defer client.Close()

	ctx := context.Background()
	rep, err := buildReport(ctx, client)
	if err != nil {
		log.Fatalf("build report: %v", err)
	}

	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(rep); err != nil {
			log.Fatalf("encode json: %v", err)
		}
		return
	}
	printReport(os.Stdout, rep)
}

func buildReport(ctx context.Context, client *neo4j.Client) (*Report, error) {
	sess := client.Driver.NewSession(ctx, driver.SessionConfig{AccessMode: driver.AccessModeRead})
	defer sess.Close(ctx)

	rep := &Report{
		GeneratedAt:      nowRFC3339(),
		Relationships:    ConfidenceBlock{ByTier: map[string]int{}},
		ByRelationType:   []RelTypeStat{},
		PerRepoExtracted: []RepoStat{},
	}

	repoCount, err := readInt(ctx, sess, `MATCH (r:Repository) RETURN count(r) AS c`, "c")
	if err != nil {
		return nil, err
	}
	rep.Repositories = repoCount

	// Relationship confidence, split by relation type. HAS_ENTITY is the
	// platform's ownership edge, not an extraction claim, so it's excluded.
	rows, err := readAll(ctx, sess, `
MATCH ()-[r]->()
WHERE type(r) <> 'HAS_ENTITY'
RETURN type(r) AS rel_type, coalesce(r.confidence, 'UNLABELED') AS confidence, count(*) AS c
`, nil)
	if err != nil {
		return nil, err
	}
	perType := map[string]*RelTypeStat{}
	for _, m := range rows {
		rel := asString(m["rel_type"])
		conf := normalizeTier(asString(m["confidence"]))
		c := asInt(m["c"])

		st, ok := perType[rel]
		if !ok {
			st = &RelTypeStat{Type: rel, ByTier: map[string]int{}}
			perType[rel] = st
		}
		st.Total += c
		st.ByTier[conf] += c

		rep.Relationships.Total += c
		rep.Relationships.ByTier[conf] += c
	}
	rep.Relationships.ExtractedShare = share(rep.Relationships.ByTier["EXTRACTED"], rep.Relationships.Total)
	for _, st := range perType {
		st.ExtractedShare = share(st.ByTier["EXTRACTED"], st.Total)
		rep.ByRelationType = append(rep.ByRelationType, *st)
	}
	sort.Slice(rep.ByRelationType, func(i, j int) bool {
		if rep.ByRelationType[i].Total != rep.ByRelationType[j].Total {
			return rep.ByRelationType[i].Total > rep.ByRelationType[j].Total
		}
		return rep.ByRelationType[i].Type < rep.ByRelationType[j].Type
	})

	// HTTP route documentation coverage.
	routeRows, err := readAll(ctx, sess, `
MATCH (n:HttpRoute)
RETURN count(*) AS total,
       sum(CASE WHEN n.documented THEN 1 ELSE 0 END) AS documented,
       sum(CASE WHEN n.source = 'openapi' THEN 1 ELSE 0 END) AS from_spec,
       sum(CASE WHEN coalesce(n.classification, '') = 'business' THEN 1 ELSE 0 END) AS business,
       sum(CASE WHEN coalesce(n.classification, '') = 'infra' THEN 1 ELSE 0 END) AS infra
`, nil)
	if err != nil {
		return nil, err
	}
	if len(routeRows) == 1 {
		m := routeRows[0]
		rs := RouteStats{
			Total:        asInt(m["total"]),
			Documented:   asInt(m["documented"]),
			FromSpecOnly: asInt(m["from_spec"]),
			Business:     asInt(m["business"]),
			Infra:        asInt(m["infra"]),
		}
		rs.DocumentedShare = share(rs.Documented, rs.Total)
		rs.BusinessShare = share(rs.Business, rs.Total)
		rep.Routes = rs
	}

	// Per-repo: entity count and the extracted-share of its stamped edges.
	// Edges are counted by their repo stamp (r.repo), so an edge this repo
	// wrote to a shared endpoint still counts here.
	repoRows, err := readAll(ctx, sess, `
MATCH (rep:Repository)
OPTIONAL MATCH (rep)-[:HAS_ENTITY]->(n:Entity)
WITH rep, count(DISTINCT n) AS entities
OPTIONAL MATCH ()-[r]->()
  WHERE r.repo = rep.name AND type(r) <> 'HAS_ENTITY'
RETURN rep.name AS repo,
       entities,
       count(r) AS edges,
       sum(CASE WHEN r.confidence = 'EXTRACTED' THEN 1 ELSE 0 END) AS extracted
ORDER BY entities DESC, repo
`, nil)
	if err != nil {
		return nil, err
	}
	for _, m := range repoRows {
		edges := asInt(m["edges"])
		rep.PerRepoExtracted = append(rep.PerRepoExtracted, RepoStat{
			Repo:           asString(m["repo"]),
			Entities:       asInt(m["entities"]),
			Edges:          edges,
			ExtractedShare: share(asInt(m["extracted"]), edges),
		})
	}

	return rep, nil
}

// normalizeTier maps a stored confidence value onto a known tier, folding any
// unexpected value into UNLABELED so the report's tiers always sum to Total.
func normalizeTier(v string) string {
	switch v {
	case "EXTRACTED", "INFERRED", "AMBIGUOUS":
		return v
	default:
		return "UNLABELED"
	}
}

func share(part, total int) float64 {
	if total == 0 {
		return 0
	}
	return float64(part) / float64(total)
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
