package httpmw

import (
	"bytes"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestWithRequestLog(t *testing.T) {
	var buf bytes.Buffer
	logger := log.New(&buf, "", 0)
	h := WithRequestLog(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTeapot)
		_, _ = w.Write([]byte("hello"))
	}), logger)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/search?q=secret", nil))
	line := buf.String()
	if !strings.Contains(line, "GET /search 418") {
		t.Errorf("log line missing method/path/status: %q", line)
	}
	if strings.Contains(line, "secret") {
		t.Errorf("query string must not be logged: %q", line)
	}
	if !strings.Contains(line, "5B") {
		t.Errorf("log line missing response size: %q", line)
	}

	buf.Reset()
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/health", nil))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/ready", nil))
	if buf.Len() != 0 {
		t.Errorf("probe endpoints must not be logged: %q", buf.String())
	}
}
