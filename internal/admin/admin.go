// Package admin is the operator surface: usage metrics, key oversight,
// indexing control, and the audit trail behind them.
//
// Every mutating action is audited with the acting identity, so the
// platform can answer "who paused indexing" and "who revoked that key"
// without reading container logs. Read-only panels are not audited - they
// would drown the trail that matters.
package admin

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"a1-knowledge-graph/internal/control"
	"a1-knowledge-graph/internal/keys"
	"a1-knowledge-graph/internal/usage"
)

// Deps are the collaborators the admin surface reads and drives.
type Deps struct {
	Usage    *usage.Reader
	Keys     *keys.Store
	Control  *control.Store
	Graph    GraphStats
	Indexing IndexingStatus
}

// GraphStats exposes graph size and freshness for the overview panel.
type GraphStats interface {
	ListRepositories(ctx context.Context) ([]RepoSize, error)
	Freshness(ctx context.Context) ([]RepoFreshness, error)
}

// IndexingStatus exposes the indexer's own view (last commit, failures).
// Optional: nil renders the panel as unavailable rather than failing.
type IndexingStatus interface {
	Snapshot(ctx context.Context) (any, error)
}

// RepoSize is one repository's entity count.
type RepoSize struct {
	Name  string `json:"name"`
	Nodes int    `json:"nodes"`
}

// RepoFreshness is one repository's index age.
type RepoFreshness struct {
	Repo       string `json:"repo"`
	AgeSeconds int64  `json:"age_seconds"`
	Stale      bool   `json:"stale"`
}

// Handler serves /admin.
type Handler struct {
	deps Deps
	auth Authorizer
	now  func() time.Time

	// anomalyWindow and anomalyThreshold tune enumeration detection: more
	// than N distinct repositories touched by one actor inside the window.
	anomalyWindow    time.Duration
	anomalyThreshold int
}

// Authorizer decides whether a request may use the admin surface and, if
// so, under what identity (for the audit trail).
type Authorizer interface {
	Authorize(r *http.Request) (actor string, ok bool)
}

// NewHandler wires the admin surface.
func NewHandler(deps Deps, auth Authorizer, window time.Duration, threshold int) *Handler {
	if window <= 0 {
		window = time.Hour
	}
	if threshold <= 0 {
		threshold = 10
	}
	return &Handler{
		deps:             deps,
		auth:             auth,
		now:              time.Now,
		anomalyWindow:    window,
		anomalyThreshold: threshold,
	}
}

// Routes returns the admin mux. Auth is applied per-handler so the audit
// entry can name the acting identity.
func (h *Handler) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /admin", h.guard(h.dashboard))
	mux.HandleFunc("GET /admin/{$}", h.guard(h.dashboard))
	mux.HandleFunc("GET /admin/api/overview", h.guard(h.overview))
	mux.HandleFunc("GET /admin/api/usage", h.guard(h.usageMetrics))
	mux.HandleFunc("GET /admin/api/anomalies", h.guard(h.anomalies))
	mux.HandleFunc("GET /admin/api/keys", h.guard(h.listKeys))
	mux.HandleFunc("GET /admin/api/audit", h.guard(h.auditTrail))
	mux.HandleFunc("POST /admin/api/keys/revoke", h.guard(h.revokeKey))
	mux.HandleFunc("POST /admin/api/indexing/pause", h.guard(h.pauseIndexing))
	mux.HandleFunc("POST /admin/api/indexing/resume", h.guard(h.resumeIndexing))
	return mux
}

type actorHandler func(w http.ResponseWriter, r *http.Request, actor string)

// guard authenticates and passes the acting identity through.
func (h *Handler) guard(next actorHandler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		actor, ok := h.auth.Authorize(r)
		if !ok {
			// HTML for a browser hitting the dashboard, JSON for API calls -
			// a signed-out admin should see a sign-in link, not a blob.
			if strings.Contains(r.Header.Get("Accept"), "text/html") {
				w.Header().Set("Content-Type", "text/html; charset=utf-8")
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = w.Write([]byte(signInPage))
				return
			}
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "admin authorization required"})
			return
		}
		next(w, r, actor)
	}
}

// -------- read panels --------

type overviewResponse struct {
	GeneratedAt  time.Time            `json:"generated_at"`
	Repos        []RepoSize           `json:"repos"`
	TotalNodes   int                  `json:"total_nodes"`
	Freshness    []RepoFreshness      `json:"freshness"`
	StaleRepos   int                  `json:"stale_repos"`
	Indexing     control.State        `json:"indexing"`
	IndexerState any                  `json:"indexer_state,omitempty"`
	Audit        []control.AuditEntry `json:"recent_audit"`
}

func (h *Handler) overview(w http.ResponseWriter, r *http.Request, _ string) {
	ctx := r.Context()
	out := overviewResponse{GeneratedAt: h.now().UTC()}

	if h.deps.Graph != nil {
		repos, err := h.deps.Graph.ListRepositories(ctx)
		if err != nil {
			serverError(w, "list repositories", err)
			return
		}
		out.Repos = repos
		for _, rp := range repos {
			out.TotalNodes += rp.Nodes
		}
		fresh, err := h.deps.Graph.Freshness(ctx)
		if err != nil {
			serverError(w, "freshness", err)
			return
		}
		out.Freshness = fresh
		for _, f := range fresh {
			if f.Stale {
				out.StaleRepos++
			}
		}
	}
	if h.deps.Control != nil {
		st, err := h.deps.Control.Get(ctx)
		if err != nil {
			serverError(w, "control state", err)
			return
		}
		out.Indexing = st
		if audit, err := h.deps.Control.RecentAudit(ctx, 20); err == nil {
			out.Audit = audit
		}
	}
	if h.deps.Indexing != nil {
		if snap, err := h.deps.Indexing.Snapshot(ctx); err == nil {
			out.IndexerState = snap
		}
	}
	writeJSON(w, http.StatusOK, out)
}

type usageResponse struct {
	Days      int              `json:"days"`
	Total     int              `json:"total_requests"`
	TopUsers  []usage.Count    `json:"top_users"`
	TopRepos  []usage.Count    `json:"top_repos"`
	Endpoints []usage.Count    `json:"top_endpoints"`
	Traffic   []usage.DayCount `json:"traffic"`
}

func (h *Handler) usageMetrics(w http.ResponseWriter, r *http.Request, _ string) {
	if h.deps.Usage == nil {
		writeJSON(w, http.StatusOK, usageResponse{})
		return
	}
	ctx := r.Context()
	days := intParam(r, "days", 30)
	out := usageResponse{Days: days}

	var err error
	if out.Total, err = h.deps.Usage.TotalRequests(ctx, days); err != nil {
		serverError(w, "total requests", err)
		return
	}
	if out.TopUsers, err = h.deps.Usage.TopUsers(ctx, days, 15); err != nil {
		serverError(w, "top users", err)
		return
	}
	if out.TopRepos, err = h.deps.Usage.TopRepos(ctx, days, 15); err != nil {
		serverError(w, "top repos", err)
		return
	}
	if out.Endpoints, err = h.deps.Usage.TopEndpoints(ctx, days, 15); err != nil {
		serverError(w, "top endpoints", err)
		return
	}
	if out.Traffic, err = h.deps.Usage.Traffic(ctx, days); err != nil {
		serverError(w, "traffic", err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *Handler) anomalies(w http.ResponseWriter, r *http.Request, _ string) {
	if h.deps.Usage == nil {
		writeJSON(w, http.StatusOK, map[string]any{"anomalies": []usage.Anomaly{}})
		return
	}
	window := h.anomalyWindow
	if m := intParam(r, "minutes", 0); m > 0 {
		window = time.Duration(m) * time.Minute
	}
	threshold := h.anomalyThreshold
	if t := intParam(r, "threshold", 0); t > 0 {
		threshold = t
	}
	found, err := h.deps.Usage.Anomalies(r.Context(), window, threshold)
	if err != nil {
		serverError(w, "anomalies", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"window_minutes": int(window.Minutes()),
		"threshold":      threshold,
		"anomalies":      found,
	})
}

func (h *Handler) listKeys(w http.ResponseWriter, r *http.Request, _ string) {
	if h.deps.Keys == nil {
		writeJSON(w, http.StatusOK, map[string]any{"keys": []keys.Key{}})
		return
	}
	all, err := h.deps.Keys.List(r.Context())
	if err != nil {
		serverError(w, "list keys", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"keys": all})
}

func (h *Handler) auditTrail(w http.ResponseWriter, r *http.Request, _ string) {
	if h.deps.Control == nil {
		writeJSON(w, http.StatusOK, map[string]any{"audit": []control.AuditEntry{}})
		return
	}
	entries, err := h.deps.Control.RecentAudit(r.Context(), intParam(r, "limit", 50))
	if err != nil {
		serverError(w, "audit", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"audit": entries})
}

// -------- actions --------

func (h *Handler) revokeKey(w http.ResponseWriter, r *http.Request, actor string) {
	var req struct {
		Owner string `json:"owner"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if strings.TrimSpace(req.Owner) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "owner required"})
		return
	}
	if h.deps.Keys == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "key store unavailable"})
		return
	}
	revoked, err := h.deps.Keys.RevokeOwn(r.Context(), req.Owner)
	if err != nil {
		serverError(w, "revoke key", err)
		return
	}
	h.audit(r.Context(), actor, "key.revoke", req.Owner)
	writeJSON(w, http.StatusOK, map[string]any{"revoked": revoked, "owner": req.Owner})
}

func (h *Handler) pauseIndexing(w http.ResponseWriter, r *http.Request, actor string) {
	var req struct {
		Reason  string `json:"reason"`
		Minutes int    `json:"minutes"` // 0 = indefinite
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if h.deps.Control == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "control store unavailable"})
		return
	}
	var until *time.Time
	if req.Minutes > 0 {
		t := h.now().UTC().Add(time.Duration(req.Minutes) * time.Minute)
		until = &t
	}
	if err := h.deps.Control.Pause(r.Context(), actor, req.Reason, until); err != nil {
		serverError(w, "pause", err)
		return
	}
	st, _ := h.deps.Control.Get(r.Context())
	writeJSON(w, http.StatusOK, st)
}

func (h *Handler) resumeIndexing(w http.ResponseWriter, r *http.Request, actor string) {
	if h.deps.Control == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "control store unavailable"})
		return
	}
	if err := h.deps.Control.Resume(r.Context(), actor); err != nil {
		serverError(w, "resume", err)
		return
	}
	st, _ := h.deps.Control.Get(r.Context())
	writeJSON(w, http.StatusOK, st)
}

func (h *Handler) audit(ctx context.Context, actor, action, detail string) {
	if h.deps.Control == nil {
		return
	}
	if err := h.deps.Control.Audit(ctx, actor, action, detail); err != nil {
		log.Printf("admin: audit write failed (%s by %s): %v", action, actor, err)
	}
}

// -------- helpers --------

func intParam(r *http.Request, name string, def int) int {
	if v := r.URL.Query().Get(name); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

func decodeJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	if r.Body == nil || r.ContentLength == 0 {
		return true // all action bodies are optional
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid body: " + err.Error()})
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("admin: write json: %v", err)
	}
}

// serverError logs the detail and returns a generic message - the same
// contract as the query API: driver text can name internal hosts.
func serverError(w http.ResponseWriter, what string, err error) {
	log.Printf("admin: %s: %v", what, err)
	writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
}

const signInPage = `<!doctype html><html><head><meta charset="utf-8">
<title>A1 Knowledge Graph - admin</title>
<style>body{font-family:system-ui,sans-serif;max-width:32rem;margin:4rem auto;padding:0 1rem;line-height:1.5}
a{color:inherit}</style></head><body>
<h1>Admin</h1>
<p>This page requires an administrator sign-in.</p>
<p><a href="/portal/login?return_to=/admin">Sign in</a></p>
</body></html>`
