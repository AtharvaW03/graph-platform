package query

import (
	"context"
	"fmt"
	"strings"

	driver "github.com/neo4j/neo4j-go-driver/v5/neo4j"
)

// Feedback is one relevance rating from an engineer, stored as (:Feedback)
// nodes in Neo4j so no separate storage system is needed.
type Feedback struct {
	// Endpoint is the feature being rated ("search", "symbol", "routes", ...).
	Endpoint string `json:"endpoint"`
	// Query is what the user asked (truncated server-side).
	Query string `json:"query"`
	// Helpful is the thumbs up (true) / down (false).
	Helpful bool `json:"helpful"`
	// Note is optional free text.
	Note string `json:"note,omitempty"`
}

// FeedbackStats aggregates ratings over a window for the quality metric.
type FeedbackStats struct {
	Days         int     `json:"days"`
	Helpful      int     `json:"helpful"`
	Unhelpful    int     `json:"unhelpful"`
	Total        int     `json:"total"`
	HelpfulShare float64 `json:"helpful_share"` // 0..1; 0 when Total == 0
}

// Field caps keep a hostile or buggy client from bloating the graph.
const (
	feedbackEndpointMax = 64
	feedbackQueryMax    = 500
	feedbackNoteMax     = 2000
)

func (s *Service) write(ctx context.Context, fn func(tx driver.ManagedTransaction) (any, error)) (any, error) {
	sess := s.db.Driver.NewSession(ctx, driver.SessionConfig{AccessMode: driver.AccessModeWrite})
	defer sess.Close(ctx)
	return sess.ExecuteWrite(ctx, fn, driver.WithTxTimeout(txTimeout))
}

// SubmitFeedback validates, truncates, and stores one rating.
func (s *Service) SubmitFeedback(ctx context.Context, f Feedback) error {
	f.Endpoint = strings.TrimSpace(f.Endpoint)
	if f.Endpoint == "" {
		return fmt.Errorf("endpoint required")
	}
	f.Endpoint = truncateRunes(f.Endpoint, feedbackEndpointMax)
	f.Query = truncateRunes(strings.TrimSpace(f.Query), feedbackQueryMax)
	f.Note = truncateRunes(strings.TrimSpace(f.Note), feedbackNoteMax)

	const cypher = `
CREATE (:Feedback {
    at:       datetime(),
    endpoint: $endpoint,
    query:    $query,
    helpful:  $helpful,
    note:     $note
})`
	_, err := s.write(ctx, func(tx driver.ManagedTransaction) (any, error) {
		res, err := tx.Run(ctx, cypher, map[string]any{
			"endpoint": f.Endpoint,
			"query":    f.Query,
			"helpful":  f.Helpful,
			"note":     f.Note,
		})
		if err != nil {
			return nil, err
		}
		return nil, res.Err()
	})
	return err
}

// GetFeedbackStats aggregates ratings over the last `days` days.
// days <= 0 defaults to 30.
func (s *Service) GetFeedbackStats(ctx context.Context, days int) (*FeedbackStats, error) {
	if days <= 0 {
		days = 30
	}
	const cypher = `
MATCH (f:Feedback)
WHERE f.at >= datetime() - duration({days: $days})
RETURN f.helpful AS helpful, count(*) AS c`

	out, err := s.read(ctx, func(tx driver.ManagedTransaction) (any, error) {
		res, err := tx.Run(ctx, cypher, map[string]any{"days": days})
		if err != nil {
			return nil, err
		}
		records, err := res.Collect(ctx)
		if err != nil {
			return nil, err
		}
		st := &FeedbackStats{Days: days}
		for _, r := range records {
			m := r.AsMap()
			c := asInt(m["c"])
			if asBool(m["helpful"]) {
				st.Helpful += c
			} else {
				st.Unhelpful += c
			}
		}
		st.Total = st.Helpful + st.Unhelpful
		if st.Total > 0 {
			st.HelpfulShare = float64(st.Helpful) / float64(st.Total)
		}
		return st, nil
	})
	if err != nil {
		return nil, err
	}
	return out.(*FeedbackStats), nil
}

// truncateRunes caps s at n runes without splitting a multi-byte character.
func truncateRunes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[:n])
}
