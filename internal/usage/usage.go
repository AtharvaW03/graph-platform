// Package usage records who queried what, so the admin dashboard can answer
// "top users", "top repositories", "is anyone enumerating the graph".
//
// Two shapes are written, deliberately:
//
//   - (:UsageStat) - one node per (day, actor, endpoint, repo), incremented
//     in place. Bounded by users x endpoints x repos x days, so it stays
//     small enough to keep indefinitely and answers every ranking query.
//   - (:RepoAccess) - one row per (actor, repo) touch with a timestamp,
//     kept for a short retention window. Day-level aggregates cannot answer
//     "35 distinct repos in 30 minutes", which is the whole point of
//     enumeration detection, so this trades a little volume for time
//     resolution and is pruned continuously.
//
// Recording is asynchronous and best-effort: a full buffer drops samples
// rather than slowing or failing a query. Metrics must never be able to
// break the read path.
package usage

import (
	"context"
	"log"
	"sync"
	"time"

	"a1-knowledge-graph/internal/neo4j"

	driver "github.com/neo4j/neo4j-go-driver/v5/neo4j"
)

const (
	// ActorInternal is the attribution for requests carrying no forwarded
	// actor: the web UI (no per-browser identity yet - the proxy injects
	// the shared service token, nothing per-user), and direct service-token
	// calls (scripts, probes, curl by an operator). Both are indistinguishable
	// today; if the web UI gets its own SSO session, split this back out.
	ActorInternal = "internal"

	flushInterval  = 5 * time.Second
	bufferSize     = 4096
	accessTTL      = 48 * time.Hour
	pruneEvery     = 12 // prune once every N flushes
	txTimeout      = 15 * time.Second
	maxReposPerHit = 8 // guard against a pathological repos= list
)

// Sample is one recorded request.
type Sample struct {
	Actor    string
	Endpoint string
	Repos    []string
	At       time.Time
}

// Recorder buffers samples and flushes them in batches.
type Recorder struct {
	db     *neo4j.Client
	ch     chan Sample
	log    *log.Logger
	now    func() time.Time
	wg     sync.WaitGroup
	stop   chan struct{}
	closed sync.Once

	// dropped counts samples discarded because the buffer was full; logged
	// on flush so an operator can see the metrics pipeline is saturated.
	mu      sync.Mutex
	dropped int
}

// NewRecorder starts the background flusher. Call Close to drain it.
func NewRecorder(db *neo4j.Client, logger *log.Logger) *Recorder {
	if logger == nil {
		logger = log.Default()
	}
	r := &Recorder{
		db:   db,
		ch:   make(chan Sample, bufferSize),
		log:  logger,
		now:  time.Now,
		stop: make(chan struct{}),
	}
	r.wg.Add(1)
	go r.loop()
	return r
}

// Record queues one sample. Never blocks; a full buffer drops the sample.
func (r *Recorder) Record(actor, endpoint string, repos []string) {
	if r == nil {
		return
	}
	if actor == "" {
		actor = ActorInternal
	}
	if len(repos) > maxReposPerHit {
		repos = repos[:maxReposPerHit]
	}
	select {
	case r.ch <- Sample{Actor: actor, Endpoint: endpoint, Repos: repos, At: r.now().UTC()}:
	default:
		r.mu.Lock()
		r.dropped++
		r.mu.Unlock()
	}
}

// Close drains the buffer and stops the flusher.
func (r *Recorder) Close() {
	if r == nil {
		return
	}
	r.closed.Do(func() {
		close(r.stop)
		r.wg.Wait()
	})
}

func (r *Recorder) loop() {
	defer r.wg.Done()
	ticker := time.NewTicker(flushInterval)
	defer ticker.Stop()
	batch := make([]Sample, 0, 256)
	flushes := 0

	drain := func() {
		for {
			select {
			case s := <-r.ch:
				batch = append(batch, s)
				if len(batch) >= cap(batch) {
					return
				}
			default:
				return
			}
		}
	}

	for {
		select {
		case <-ticker.C:
			drain()
			if len(batch) > 0 {
				r.flush(batch)
				batch = batch[:0]
			}
			flushes++
			if flushes%pruneEvery == 0 {
				r.prune()
			}
		case <-r.stop:
			drain()
			if len(batch) > 0 {
				r.flush(batch)
			}
			return
		}
	}
}

// flush folds the batch into aggregate rows before writing, so a burst of
// identical requests becomes one increment rather than N.
func (r *Recorder) flush(batch []Sample) {
	type statKey struct{ day, actor, endpoint, repo string }
	stats := make(map[statKey]int, len(batch))
	accesses := make([]map[string]any, 0, len(batch))

	for _, s := range batch {
		day := s.At.Format("2006-01-02")
		if len(s.Repos) == 0 {
			stats[statKey{day, s.Actor, s.Endpoint, ""}]++
			continue
		}
		for _, repo := range s.Repos {
			stats[statKey{day, s.Actor, s.Endpoint, repo}]++
			accesses = append(accesses, map[string]any{
				"actor": s.Actor,
				"repo":  repo,
				"at":    s.At.Format(time.RFC3339),
			})
		}
	}

	rows := make([]map[string]any, 0, len(stats))
	for k, n := range stats {
		rows = append(rows, map[string]any{
			"key":      k.day + "|" + k.actor + "|" + k.endpoint + "|" + k.repo,
			"day":      k.day,
			"actor":    k.actor,
			"endpoint": k.endpoint,
			"repo":     k.repo,
			"n":        n,
		})
	}

	ctx, cancel := context.WithTimeout(context.Background(), txTimeout)
	defer cancel()

	const statCypher = `
UNWIND $rows AS row
MERGE (u:UsageStat {key: row.key})
ON CREATE SET u.day = date(row.day), u.actor = row.actor,
              u.endpoint = row.endpoint, u.repo = row.repo, u.count = row.n
ON MATCH SET  u.count = u.count + row.n`
	if err := r.write(ctx, statCypher, map[string]any{"rows": rows}); err != nil {
		r.log.Printf("usage: stat flush failed (%d rows): %v", len(rows), err)
	}

	if len(accesses) > 0 {
		const accessCypher = `
UNWIND $rows AS row
CREATE (:RepoAccess {actor: row.actor, repo: row.repo, at: datetime(row.at)})`
		if err := r.write(ctx, accessCypher, map[string]any{"rows": accesses}); err != nil {
			r.log.Printf("usage: access flush failed (%d rows): %v", len(accesses), err)
		}
	}

	r.mu.Lock()
	if r.dropped > 0 {
		r.log.Printf("usage: dropped %d samples (buffer full)", r.dropped)
		r.dropped = 0
	}
	r.mu.Unlock()
}

// prune drops RepoAccess rows past the retention window. UsageStat rows are
// aggregates and are kept.
func (r *Recorder) prune() {
	ctx, cancel := context.WithTimeout(context.Background(), txTimeout)
	defer cancel()
	const cypher = `
MATCH (a:RepoAccess) WHERE a.at < datetime($cutoff)
CALL (a) { DETACH DELETE a } IN TRANSACTIONS OF 1000 ROWS`
	cutoff := r.now().UTC().Add(-accessTTL).Format(time.RFC3339)
	if err := r.writeAutocommit(ctx, cypher, map[string]any{"cutoff": cutoff}); err != nil {
		r.log.Printf("usage: prune failed: %v", err)
	}
}

func (r *Recorder) write(ctx context.Context, cypher string, params map[string]any) error {
	sess := r.db.Driver.NewSession(ctx, driver.SessionConfig{AccessMode: driver.AccessModeWrite})
	defer sess.Close(ctx)
	_, err := sess.ExecuteWrite(ctx, func(tx driver.ManagedTransaction) (any, error) {
		res, err := tx.Run(ctx, cypher, params)
		if err != nil {
			return nil, err
		}
		return nil, res.Err()
	}, driver.WithTxTimeout(txTimeout))
	return err
}

// writeAutocommit is for CALL { ... } IN TRANSACTIONS, which cannot run
// inside an explicit transaction.
func (r *Recorder) writeAutocommit(ctx context.Context, cypher string, params map[string]any) error {
	sess := r.db.Driver.NewSession(ctx, driver.SessionConfig{AccessMode: driver.AccessModeWrite})
	defer sess.Close(ctx)
	res, err := sess.Run(ctx, cypher, params)
	if err != nil {
		return err
	}
	_, err = res.Consume(ctx)
	return err
}
