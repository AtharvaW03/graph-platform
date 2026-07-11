package api

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"graph-platform/internal/query"
)

// WithAuth wraps h with static bearer-token authentication. An empty token
// disables auth (open mode, for local development). /health and /ready stay
// unauthenticated so load balancers and uptime probes work without
// credentials.
func WithAuth(h http.Handler, token string) http.Handler {
	if token == "" {
		return h
	}
	expected := []byte("Bearer " + token)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" || r.URL.Path == "/ready" {
			h.ServeHTTP(w, r)
			return
		}
		got := []byte(r.Header.Get("Authorization"))
		if subtle.ConstantTimeCompare(got, expected) != 1 {
			writeErr(w, http.StatusUnauthorized, "missing or invalid bearer token")
			return
		}
		h.ServeHTTP(w, r)
	})
}

// WithRequestTimeout wraps h so every request context carries a deadline.
// The deadline propagates through r.Context() into the Cypher transaction,
// so a request that outlives it is canceled at the database rather than
// holding a connection (and a Neo4j session) open indefinitely.
func WithRequestTimeout(h http.Handler, d time.Duration) http.Handler {
	if d <= 0 {
		return h
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), d)
		defer cancel()
		h.ServeHTTP(w, r.WithContext(ctx))
	})
}

// WithCORS wraps h with a minimal CORS policy for a single trusted origin.
// origin == "" disables CORS (same-origin deployments don't need it). The
// Authorization header forces a preflight, so OPTIONS is answered here.
func WithCORS(h http.Handler, origin string) http.Handler {
	if origin == "" {
		return h
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Origin") == origin {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Vary", "Origin")
			if r.Method == http.MethodOptions {
				w.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")
				w.Header().Set("Access-Control-Allow-Headers", "Authorization")
				w.Header().Set("Access-Control-Max-Age", "600")
				w.WriteHeader(http.StatusNoContent)
				return
			}
		}
		h.ServeHTTP(w, r)
	})
}

// Readiness pings a downstream dependency for GET /ready. *neo4j.Client
// already implements this (same shape as internal/index's HealthChecker);
// tests can swap in a fake.
type Readiness interface {
	VerifyConnectivity(ctx context.Context) error
}

// readyTimeout bounds the /ready connectivity check independent of the
// caller's request timeout - a readiness probe should fail fast, not hang
// for the full request budget.
const readyTimeout = 2 * time.Second

type Server struct {
	svc   *query.Service
	ready Readiness
}

// NewServer wires the query routes. ready is used by GET /ready; pass nil to
// make it always report ready (e.g. a test server with no real dependency).
func NewServer(svc *query.Service, ready Readiness) *Server {
	return &Server{svc: svc, ready: ready}
}

// Routes returns the HTTP handler with all read-only query routes mounted.
func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", s.health)
	mux.HandleFunc("GET /ready", s.readyCheck)
	mux.HandleFunc("GET /repos", s.listRepos)
	mux.HandleFunc("GET /search", s.search)
	mux.HandleFunc("GET /symbol/{name}", s.findSymbol)
	mux.HandleFunc("GET /callers/{symbol}", s.findCallers)
	mux.HandleFunc("GET /callees/{symbol}", s.findCallees)
	mux.HandleFunc("GET /blast-radius/{symbol}", s.blastRadius)
	mux.HandleFunc("GET /path", s.shortestPath)

	mux.HandleFunc("GET /overview/{repo}", s.repositoryOverview)

	mux.HandleFunc("GET /dependencies/{repo}", s.findDependencies)
	mux.HandleFunc("GET /dependents/{dep}", s.findDependents)
	mux.HandleFunc("GET /routes", s.findRoutes)
	mux.HandleFunc("GET /kafka/topic/{name}", s.findKafkaTopic)
	mux.HandleFunc("GET /sql/object", s.findSQLObject)
	mux.HandleFunc("GET /glue/jobs", s.findGlueJobs)

	mux.HandleFunc("GET /hotspots", s.findHotspots)

	mux.HandleFunc("POST /feedback", s.submitFeedback)
	mux.HandleFunc("GET /feedback/stats", s.feedbackStats)
	return mux
}

// health is pure liveness: it never touches Neo4j, so it answers even while
// the database is unreachable. Use /ready to check the dependency.
func (s *Server) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// readyCheck is dependency-aware: it pings Neo4j with a short timeout so a
// load balancer can stop routing traffic here while the database is down,
// without waiting out a full request timeout to find out.
func (s *Server) readyCheck(w http.ResponseWriter, r *http.Request) {
	if s.ready == nil {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), readyTimeout)
	defer cancel()
	if err := s.ready.VerifyConnectivity(ctx); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "not_ready", "reason": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
}

// parseRepos reads the optional repository scope from a request: `repos` is
// comma-separated, and a single `repo` value merges in alongside it.
func parseRepos(q url.Values) []string {
	raw := q.Get("repos")
	if v := q.Get("repo"); v != "" {
		if raw != "" {
			raw += ","
		}
		raw += v
	}
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func (s *Server) listRepos(w http.ResponseWriter, r *http.Request) {
	out, err := s.svc.ListRepositories(r.Context())
	if err != nil {
		serverError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) search(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query().Get("q")
	if q == "" {
		writeErr(w, http.StatusBadRequest, "missing query parameter q")
		return
	}
	results, err := s.svc.Search(r.Context(), q, parseRepos(r.URL.Query()))
	if err != nil {
		serverError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, results)
}

func (s *Server) findSymbol(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	results, err := s.svc.FindSymbol(r.Context(), name, parseRepos(r.URL.Query()))
	if err != nil {
		serverError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, results)
}

func (s *Server) findCallers(w http.ResponseWriter, r *http.Request) {
	sym := r.PathValue("symbol")
	edges, err := s.svc.FindCallers(r.Context(), sym, parseRepos(r.URL.Query()))
	if err != nil {
		serverError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, edges)
}

func (s *Server) findCallees(w http.ResponseWriter, r *http.Request) {
	sym := r.PathValue("symbol")
	edges, err := s.svc.FindCallees(r.Context(), sym, parseRepos(r.URL.Query()))
	if err != nil {
		serverError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, edges)
}

func (s *Server) blastRadius(w http.ResponseWriter, r *http.Request) {
	sym := r.PathValue("symbol")
	depth := 0
	if d := r.URL.Query().Get("depth"); d != "" {
		parsed, err := strconv.Atoi(d)
		if err != nil {
			writeErr(w, http.StatusBadRequest, "depth must be an integer")
			return
		}
		depth = parsed
	}
	nodes, err := s.svc.BlastRadius(r.Context(), sym, depth, parseRepos(r.URL.Query()))
	if err != nil {
		serverError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, nodes)
}

func (s *Server) shortestPath(w http.ResponseWriter, r *http.Request) {
	src := r.URL.Query().Get("src")
	dst := r.URL.Query().Get("dst")
	if src == "" || dst == "" {
		writeErr(w, http.StatusBadRequest, "missing src or dst")
		return
	}
	path, err := s.svc.ShortestPath(r.Context(), src, dst, parseRepos(r.URL.Query()))
	if err != nil {
		serverError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, path)
}

func (s *Server) repositoryOverview(w http.ResponseWriter, r *http.Request) {
	repo := r.PathValue("repo")
	if repo == "" {
		writeErr(w, http.StatusBadRequest, "missing repo")
		return
	}
	out, err := s.svc.RepositoryOverview(r.Context(), repo)
	if err != nil {
		serverError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) findDependencies(w http.ResponseWriter, r *http.Request) {
	repo := r.PathValue("repo")
	scope := r.URL.Query().Get("scope")
	out, err := s.svc.FindDependencies(r.Context(), repo, scope)
	if err != nil {
		serverError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) findDependents(w http.ResponseWriter, r *http.Request) {
	dep := r.PathValue("dep")
	out, err := s.svc.FindDependents(r.Context(), dep)
	if err != nil {
		serverError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) findRoutes(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	out, err := s.svc.FindRoutes(r.Context(), q.Get("method"), q.Get("path"), parseRepos(q))
	if err != nil {
		serverError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) findKafkaTopic(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	out, err := s.svc.FindKafkaTopic(r.Context(), name)
	if err != nil {
		serverError(w, err)
		return
	}
	if out == nil {
		writeErr(w, http.StatusNotFound, "topic not found")
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) findSQLObject(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	out, err := s.svc.FindSQLObject(r.Context(), q.Get("schema"), q.Get("name"))
	if err != nil {
		serverError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) findGlueJobs(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	out, err := s.svc.FindGlueJobs(r.Context(), q.Get("source"), q.Get("target"))
	if err != nil {
		serverError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) findHotspots(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit := 0
	if l := q.Get("limit"); l != "" {
		parsed, err := strconv.Atoi(l)
		if err != nil || parsed <= 0 {
			writeErr(w, http.StatusBadRequest, "limit must be a positive integer")
			return
		}
		limit = parsed
	}
	out, err := s.svc.FindHotspots(r.Context(), parseRepos(q), limit)
	if err != nil {
		serverError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// submitFeedback records one thumbs up/down; the only write endpoint on an
// otherwise read-only API. Body is size-capped and validated downstream.
func (s *Server) submitFeedback(w http.ResponseWriter, r *http.Request) {
	var f query.Feedback
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&f); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid feedback body: "+err.Error())
		return
	}
	if strings.TrimSpace(f.Endpoint) == "" {
		writeErr(w, http.StatusBadRequest, "endpoint required")
		return
	}
	if err := s.svc.SubmitFeedback(r.Context(), f); err != nil {
		serverError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) feedbackStats(w http.ResponseWriter, r *http.Request) {
	days := 30
	if d := r.URL.Query().Get("days"); d != "" {
		parsed, err := strconv.Atoi(d)
		if err != nil || parsed <= 0 {
			writeErr(w, http.StatusBadRequest, "days must be a positive integer")
			return
		}
		days = parsed
	}
	out, err := s.svc.GetFeedbackStats(r.Context(), days)
	if err != nil {
		serverError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("write json: %v", err)
	}
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

func serverError(w http.ResponseWriter, err error) {
	if errors.Is(err, query.ErrNotImplemented) {
		writeErr(w, http.StatusNotImplemented, err.Error())
		return
	}
	log.Printf("query error: %v", err)
	writeErr(w, http.StatusInternalServerError, "internal error")
}
