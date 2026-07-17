package index

import (
	"encoding/json"
	"net/http"
	"sort"
	"time"
)

// statusResponse is the JSON body served by NewStatusHandler. It answers "is
// the graph caught up?" per repository: what commit was last indexed and
// when, whether the last attempt succeeded, and whether a webhook delivery
// is queued but not yet processed. A repo that shows last_status=success,
// pending_reindex=false, and a recent last_attempt_at is fully caught up -
// the reconciliation sweep re-checks every repo's remote HEAD at least once
// per sweep interval, so "recent" means within one sweep of now.
type statusResponse struct {
	GeneratedAt  time.Time    `json:"generated_at"`
	Repositories []repoStatus `json:"repositories"`
}

type repoStatus struct {
	Name              string     `json:"name"`
	Branch            string     `json:"branch,omitempty"`
	LastStatus        Status     `json:"last_status,omitempty"`
	LastStage         Stage      `json:"last_stage,omitempty"`
	LastIndexedCommit string     `json:"last_indexed_commit,omitempty"`
	LastIndexedAt     *time.Time `json:"last_indexed_at,omitempty"`
	LastAttemptAt     *time.Time `json:"last_attempt_at,omitempty"`
	LastError         string     `json:"last_error,omitempty"`
	ConsecutiveFails  int        `json:"consecutive_fails,omitempty"`
	// PendingReindex is true when a webhook delivery has queued this repo but
	// the indexing loop hasn't picked it up yet.
	PendingReindex bool `json:"pending_reindex"`
}

// NewStatusHandler serves a read-only JSON snapshot of per-repo indexing
// state. source serves the current manifest (so never-attempted repos still
// appear, with empty state, and discovery-driven additions show up without a
// restart); pending may be nil when webhook mode is off.
func NewStatusHandler(source JobSource, store StateStore, pending *PendingSet) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		repos, err := source.Repositories(r.Context())
		if err != nil {
			http.Error(w, "repository manifest unavailable", http.StatusServiceUnavailable)
			return
		}
		queued := map[string]bool{}
		if pending != nil {
			for _, n := range pending.Snapshot() {
				queued[n] = true
			}
		}

		out := statusResponse{GeneratedAt: time.Now().UTC()}
		for _, repo := range repos {
			st, _ := store.Get(repo.Name)
			rs := repoStatus{
				Name:              repo.Name,
				Branch:            repo.Branch,
				LastStatus:        st.LastStatus,
				LastStage:         st.LastStage,
				LastIndexedCommit: st.LastIndexedCommit,
				LastError:         st.LastError,
				ConsecutiveFails:  st.ConsecutiveFails,
				PendingReindex:    queued[repo.Name],
			}
			if !st.LastIndexedAt.IsZero() {
				t := st.LastIndexedAt
				rs.LastIndexedAt = &t
			}
			if !st.LastAttemptAt.IsZero() {
				t := st.LastAttemptAt
				rs.LastAttemptAt = &t
			}
			out.Repositories = append(out.Repositories, rs)
		}
		sort.Slice(out.Repositories, func(i, j int) bool {
			return out.Repositories[i].Name < out.Repositories[j].Name
		})

		w.Header().Set("Content-Type", "application/json")
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		_ = enc.Encode(out)
	})
}
