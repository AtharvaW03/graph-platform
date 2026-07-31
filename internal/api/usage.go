package api

import (
	"net/http"
	"strings"

	"a1-knowledge-graph/internal/httpmw"
	"a1-knowledge-graph/internal/usage"
)

// UsageRecorder is the slice of *usage.Recorder this package needs.
type UsageRecorder interface {
	Record(actor, endpoint string, repos []string)
}

// WithUsageRecording records one attributed sample per successful query
// request. Mount INSIDE auth (so only authenticated traffic is counted) and
// inside WithForwardedActor (so the actor is already resolved).
//
// Only read endpoints that name a subject are recorded: health/ready
// probes, the portal, admin views, and key validation are infrastructure
// chatter, not user activity, and counting them would drown the rankings.
func WithUsageRecording(h http.Handler, rec UsageRecorder) http.Handler {
	if rec == nil {
		return h
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		endpoint, ok := recordableEndpoint(r.URL.Path)
		if !ok {
			h.ServeHTTP(w, r)
			return
		}
		// Wrap so a failed request isn't counted as usage - a 401 or 500 is
		// not someone reading the graph.
		sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		h.ServeHTTP(sw, r)
		if sw.status >= 400 {
			return
		}

		actor := httpmw.ActorFrom(r.Context())
		if actor == "" {
			actor = usage.ActorInternal
		}
		rec.Record(actor, endpoint, requestRepos(r))
	})
}

// statusWriter captures the status code without buffering the body.
type statusWriter struct {
	http.ResponseWriter
	status  int
	written bool
}

func (w *statusWriter) WriteHeader(code int) {
	if !w.written {
		w.status = code
		w.written = true
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	w.written = true
	return w.ResponseWriter.Write(b)
}

// recordableEndpoint maps a request path to its metric name, or reports
// false for paths that shouldn't count as usage.
func recordableEndpoint(path string) (string, bool) {
	trimmed := strings.Trim(path, "/")
	if trimmed == "" {
		return "", false
	}
	first, _, _ := strings.Cut(trimmed, "/")
	switch first {
	case "health", "ready", "portal", "admin", "keys":
		return "", false
	}
	// Second segment is the subject (a symbol name, a repo) - never part of
	// the metric name, which would explode cardinality and leak search terms
	// into stored data.
	return first, true
}

// requestRepos extracts the repository scope a request asked for, so
// "which repos does this person touch" is answerable. Path-scoped
// endpoints (/overview/{repo}, /dependencies/{repo}) carry it in the path;
// everything else uses repo=/repos=.
func requestRepos(r *http.Request) []string {
	if v := r.PathValue("repo"); v != "" {
		return []string{v}
	}
	q := r.URL.Query()
	out := make([]string, 0, 4)
	for _, raw := range append(q["repo"], q["repos"]...) {
		for _, name := range strings.Split(raw, ",") {
			if name = strings.TrimSpace(name); name != "" {
				out = append(out, name)
			}
		}
	}
	return out
}
