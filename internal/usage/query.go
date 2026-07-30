package usage

import (
	"context"
	"time"

	"a1-knowledge-graph/internal/neo4j"

	driver "github.com/neo4j/neo4j-go-driver/v5/neo4j"
)

// Reader answers the dashboard's metric queries. Separate from Recorder so
// the read path carries no buffering machinery.
type Reader struct {
	db  *neo4j.Client
	now func() time.Time
}

func NewReader(db *neo4j.Client) *Reader {
	return &Reader{db: db, now: time.Now}
}

// Count is one ranked row: a name and its request total.
type Count struct {
	Name  string `json:"name"`
	Count int    `json:"count"`
}

// DayCount is one point on the traffic timeseries.
type DayCount struct {
	Day   string `json:"day"`
	Count int    `json:"count"`
}

// Anomaly is one actor whose distinct-repository reach in the detection
// window crossed the threshold - the enumeration signal.
type Anomaly struct {
	Actor     string   `json:"actor"`
	RepoCount int      `json:"repo_count"`
	Requests  int      `json:"requests"`
	Repos     []string `json:"repos"`
	FirstSeen string   `json:"first_seen"`
	LastSeen  string   `json:"last_seen"`
}

// TopUsers ranks actors by request volume over the last days.
func (r *Reader) TopUsers(ctx context.Context, days, limit int) ([]Count, error) {
	return r.rankBy(ctx, "actor", days, limit)
}

// TopRepos ranks repositories by how often they were queried.
func (r *Reader) TopRepos(ctx context.Context, days, limit int) ([]Count, error) {
	return r.rankBy(ctx, "repo", days, limit)
}

// TopEndpoints ranks endpoints by call volume.
func (r *Reader) TopEndpoints(ctx context.Context, days, limit int) ([]Count, error) {
	return r.rankBy(ctx, "endpoint", days, limit)
}

func (r *Reader) rankBy(ctx context.Context, field string, days, limit int) ([]Count, error) {
	days, limit = boundWindow(days), boundLimit(limit)
	// field is never caller-supplied - the three exported wrappers above are
	// the only callers and pass literals.
	cypher := `
MATCH (u:UsageStat)
WHERE u.day >= date($from) AND u.` + field + ` <> ''
RETURN u.` + field + ` AS name, sum(u.count) AS n
ORDER BY n DESC
LIMIT $limit`
	out := make([]Count, 0, limit)
	err := r.read(ctx, cypher, map[string]any{
		"from":  r.fromDay(days),
		"limit": limit,
	}, func(m map[string]any) {
		out = append(out, Count{Name: asString(m["name"]), Count: asInt(m["n"])})
	})
	return out, err
}

// Traffic returns per-day request totals, oldest first.
func (r *Reader) Traffic(ctx context.Context, days int) ([]DayCount, error) {
	days = boundWindow(days)
	const cypher = `
MATCH (u:UsageStat)
WHERE u.day >= date($from)
RETURN toString(u.day) AS day, sum(u.count) AS n
ORDER BY day ASC`
	out := make([]DayCount, 0, days)
	err := r.read(ctx, cypher, map[string]any{"from": r.fromDay(days)}, func(m map[string]any) {
		out = append(out, DayCount{Day: asString(m["day"]), Count: asInt(m["n"])})
	})
	return out, err
}

// TotalRequests is the request count over the window.
func (r *Reader) TotalRequests(ctx context.Context, days int) (int, error) {
	days = boundWindow(days)
	const cypher = `
MATCH (u:UsageStat) WHERE u.day >= date($from)
RETURN sum(u.count) AS n`
	total := 0
	err := r.read(ctx, cypher, map[string]any{"from": r.fromDay(days)}, func(m map[string]any) {
		total = asInt(m["n"])
	})
	return total, err
}

// Anomalies returns actors who touched more than threshold distinct
// repositories within the trailing window. This is the "someone is
// enumerating everything before they leave" detector: normal work touches a
// handful of repositories, a sweep touches all of them.
func (r *Reader) Anomalies(ctx context.Context, window time.Duration, threshold int) ([]Anomaly, error) {
	if window <= 0 {
		window = time.Hour
	}
	if threshold <= 0 {
		threshold = 10
	}
	// actor <> $sharedActor excludes the catch-all bucket every unattributed
	// caller shares (web UI, direct script access): it aggregates many real
	// people's activity, so it can cross any repo-count threshold on volume
	// alone without being one person's behavior - a false positive, not a
	// signal. A real bug here previously excluded a constant that was never
	// actually stored as an actor value, so this exclusion was a no-op.
	const cypher = `
MATCH (a:RepoAccess)
WHERE a.at >= datetime($since) AND a.actor <> $sharedActor
WITH a.actor AS actor, collect(DISTINCT a.repo) AS repos, count(a) AS requests,
     min(a.at) AS first_seen, max(a.at) AS last_seen
WHERE size(repos) >= $threshold
RETURN actor, repos, requests,
       toString(first_seen) AS first_seen, toString(last_seen) AS last_seen
ORDER BY size(repos) DESC
LIMIT 50`
	out := make([]Anomaly, 0, 8)
	err := r.read(ctx, cypher, map[string]any{
		"since":       r.now().UTC().Add(-window).Format(time.RFC3339),
		"threshold":   threshold,
		"sharedActor": ActorInternal,
	}, func(m map[string]any) {
		repos := asStrings(m["repos"])
		out = append(out, Anomaly{
			Actor:     asString(m["actor"]),
			RepoCount: len(repos),
			Requests:  asInt(m["requests"]),
			Repos:     repos,
			FirstSeen: asString(m["first_seen"]),
			LastSeen:  asString(m["last_seen"]),
		})
	})
	return out, err
}

// ActorActivity summarizes one actor: request total and repositories
// touched in the window. Backs the per-user drill-down.
func (r *Reader) ActorActivity(ctx context.Context, actor string, days int) ([]Count, error) {
	days = boundWindow(days)
	const cypher = `
MATCH (u:UsageStat)
WHERE u.day >= date($from) AND u.actor = $actor AND u.repo <> ''
RETURN u.repo AS name, sum(u.count) AS n
ORDER BY n DESC
LIMIT 50`
	out := make([]Count, 0, 16)
	err := r.read(ctx, cypher, map[string]any{"from": r.fromDay(days), "actor": actor}, func(m map[string]any) {
		out = append(out, Count{Name: asString(m["name"]), Count: asInt(m["n"])})
	})
	return out, err
}

func (r *Reader) fromDay(days int) string {
	return r.now().UTC().AddDate(0, 0, -(days - 1)).Format("2006-01-02")
}

func boundWindow(days int) int {
	if days <= 0 {
		return 30
	}
	if days > 365 {
		return 365
	}
	return days
}

func boundLimit(limit int) int {
	if limit <= 0 {
		return 10
	}
	if limit > 100 {
		return 100
	}
	return limit
}

func (r *Reader) read(ctx context.Context, cypher string, params map[string]any, fn func(map[string]any)) error {
	sess := r.db.Driver.NewSession(ctx, driver.SessionConfig{AccessMode: driver.AccessModeRead})
	defer sess.Close(ctx)
	_, err := sess.ExecuteRead(ctx, func(tx driver.ManagedTransaction) (any, error) {
		res, err := tx.Run(ctx, cypher, params)
		if err != nil {
			return nil, err
		}
		records, err := res.Collect(ctx)
		if err != nil {
			return nil, err
		}
		for _, rec := range records {
			fn(rec.AsMap())
		}
		return nil, nil
	}, driver.WithTxTimeout(txTimeout))
	return err
}

func asString(v any) string {
	s, _ := v.(string)
	return s
}

func asInt(v any) int {
	if i, ok := v.(int64); ok {
		return int(i)
	}
	return 0
}

func asStrings(v any) []string {
	raw, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(raw))
	for _, item := range raw {
		if s, ok := item.(string); ok && s != "" {
			out = append(out, s)
		}
	}
	return out
}
