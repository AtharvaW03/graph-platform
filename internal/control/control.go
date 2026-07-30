// Package control holds operator control state shared between the admin
// surface (on query-service) and the indexer: a pause switch, plus the
// append-only audit trail of who changed what.
//
// State lives in Neo4j rather than behind an indexer HTTP endpoint on
// purpose: the indexer has no reachable port in interval-only mode, both
// processes already share the database, and a pause survives a task
// restart. Pause is advisory at repository boundaries - never mid-import,
// which would violate the fail-closed invariant (a half-imported repo lets
// the next sweep delete good data).
package control

import (
	"context"
	"fmt"
	"strings"
	"time"

	"a1-knowledge-graph/internal/neo4j"

	driver "github.com/neo4j/neo4j-go-driver/v5/neo4j"
)

const (
	controlKey = "indexer"
	txTimeout  = 10 * time.Second
	auditLimit = 200
)

// State is the indexer's operator-controlled run state.
type State struct {
	Paused      bool       `json:"paused"`
	PausedUntil *time.Time `json:"paused_until,omitempty"` // nil = indefinite
	Actor       string     `json:"actor,omitempty"`
	Reason      string     `json:"reason,omitempty"`
	ChangedAt   time.Time  `json:"changed_at,omitzero"`
}

// PausedNow reports whether indexing should be held right now, resolving a
// timed pause that has expired back to running.
func (s State) PausedNow(now time.Time) bool {
	if !s.Paused {
		return false
	}
	if s.PausedUntil != nil && !now.Before(*s.PausedUntil) {
		return false
	}
	return true
}

// Describe renders the state for a log line or dashboard row.
func (s State) Describe() string {
	if !s.Paused {
		return "running"
	}
	if s.PausedUntil != nil {
		return fmt.Sprintf("paused until %s by %s", s.PausedUntil.UTC().Format(time.RFC3339), s.Actor)
	}
	return fmt.Sprintf("paused indefinitely by %s", s.Actor)
}

// AuditEntry is one privileged action.
type AuditEntry struct {
	At     time.Time `json:"at"`
	Actor  string    `json:"actor"`
	Action string    `json:"action"`
	Detail string    `json:"detail,omitempty"`
}

// Store reads and writes control state and the audit trail.
type Store struct {
	db  *neo4j.Client
	now func() time.Time
}

func NewStore(db *neo4j.Client) *Store {
	return &Store{db: db, now: time.Now}
}

// Get returns the current state. A never-configured platform reads as
// running, so indexing works before anyone touches the control surface.
func (s *Store) Get(ctx context.Context) (State, error) {
	const cypher = `
MATCH (c:IndexerControl {key: $key})
RETURN c.paused AS paused, c.paused_until AS until, c.actor AS actor,
       c.reason AS reason, c.changed_at AS changed_at`
	var st State
	err := s.read(ctx, cypher, map[string]any{"key": controlKey}, func(m map[string]any) {
		st.Paused, _ = m["paused"].(bool)
		st.Actor = asString(m["actor"])
		st.Reason = asString(m["reason"])
		st.ChangedAt = asTime(m["changed_at"])
		if t := asTime(m["until"]); !t.IsZero() {
			st.PausedUntil = &t
		}
	})
	return st, err
}

// Pause halts indexing at the next repository boundary. until == nil pauses
// indefinitely; a non-nil until resumes automatically at that instant
// (the "stop indexing at 9pm, resume overnight" case).
func (s *Store) Pause(ctx context.Context, actor, reason string, until *time.Time) error {
	params := map[string]any{
		"key":    controlKey,
		"actor":  actor,
		"reason": strings.TrimSpace(reason),
		"now":    s.now().UTC().Format(time.RFC3339),
		"until":  nil,
	}
	if until != nil {
		params["until"] = until.UTC().Format(time.RFC3339)
	}
	const cypher = `
MERGE (c:IndexerControl {key: $key})
SET c.paused = true, c.actor = $actor, c.reason = $reason,
    c.changed_at = datetime($now),
    c.paused_until = CASE WHEN $until IS NULL THEN NULL ELSE datetime($until) END`
	if err := s.write(ctx, cypher, params); err != nil {
		return err
	}
	detail := "indefinite"
	if until != nil {
		detail = "until " + until.UTC().Format(time.RFC3339)
	}
	if reason != "" {
		detail += " - " + reason
	}
	return s.Audit(ctx, actor, "indexing.pause", detail)
}

// Resume clears the pause.
func (s *Store) Resume(ctx context.Context, actor string) error {
	const cypher = `
MERGE (c:IndexerControl {key: $key})
SET c.paused = false, c.paused_until = NULL, c.actor = $actor,
    c.reason = '', c.changed_at = datetime($now)`
	if err := s.write(ctx, cypher, map[string]any{
		"key":   controlKey,
		"actor": actor,
		"now":   s.now().UTC().Format(time.RFC3339),
	}); err != nil {
		return err
	}
	return s.Audit(ctx, actor, "indexing.resume", "")
}

// Audit appends one privileged-action record. Callers should audit every
// mutating admin action, including ones that fail authorization checks
// upstream of here.
func (s *Store) Audit(ctx context.Context, actor, action, detail string) error {
	const cypher = `
CREATE (:AdminAudit {at: datetime($now), actor: $actor, action: $action, detail: $detail})`
	return s.write(ctx, cypher, map[string]any{
		"now":    s.now().UTC().Format(time.RFC3339),
		"actor":  actor,
		"action": action,
		"detail": detail,
	})
}

// RecentAudit returns the newest audit entries, most recent first.
func (s *Store) RecentAudit(ctx context.Context, limit int) ([]AuditEntry, error) {
	if limit <= 0 || limit > auditLimit {
		limit = 50
	}
	const cypher = `
MATCH (a:AdminAudit)
RETURN a.at AS at, a.actor AS actor, a.action AS action, a.detail AS detail
ORDER BY at DESC
LIMIT $limit`
	out := make([]AuditEntry, 0, limit)
	err := s.read(ctx, cypher, map[string]any{"limit": limit}, func(m map[string]any) {
		out = append(out, AuditEntry{
			At:     asTime(m["at"]),
			Actor:  asString(m["actor"]),
			Action: asString(m["action"]),
			Detail: asString(m["detail"]),
		})
	})
	return out, err
}

func (s *Store) write(ctx context.Context, cypher string, params map[string]any) error {
	sess := s.db.Driver.NewSession(ctx, driver.SessionConfig{AccessMode: driver.AccessModeWrite})
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

func (s *Store) read(ctx context.Context, cypher string, params map[string]any, fn func(map[string]any)) error {
	sess := s.db.Driver.NewSession(ctx, driver.SessionConfig{AccessMode: driver.AccessModeRead})
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

func asTime(v any) time.Time {
	if t, ok := v.(time.Time); ok {
		return t.UTC()
	}
	return time.Time{}
}
