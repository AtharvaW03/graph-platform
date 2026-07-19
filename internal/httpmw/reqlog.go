package httpmw

import (
	"log"
	"net/http"
	"time"
)

// WithRequestLog logs one line per request: method, path, status, duration,
// and response size. /health and /ready are skipped so load-balancer probes
// don't flood the log. Query strings are not logged.
func WithRequestLog(h http.Handler, logger *log.Logger) http.Handler {
	if logger == nil {
		logger = log.Default()
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" || r.URL.Path == "/ready" {
			h.ServeHTTP(w, r)
			return
		}
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		h.ServeHTTP(rec, r)
		logger.Printf("%s %s %d %s %dB", r.Method, r.URL.Path, rec.status, time.Since(start).Round(time.Millisecond), rec.bytes)
	})
}

// statusRecorder captures the response status and size for logging.
type statusRecorder struct {
	http.ResponseWriter
	status int
	bytes  int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

func (r *statusRecorder) Write(b []byte) (int, error) {
	n, err := r.ResponseWriter.Write(b)
	r.bytes += n
	return n, err
}

// Flush forwards to the underlying writer when it supports streaming
// responses (the MCP transport uses SSE).
func (r *statusRecorder) Flush() {
	if f, ok := r.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}
