package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"graph-platform/internal/httpmw"
	"graph-platform/internal/mcp"
)

func main() {
	// stdio is reserved for the MCP transport; logs go to stderr.
	log.SetOutput(os.Stderr)

	baseURL := envOr("QUERY_SERVICE_URL", "http://localhost:8080")

	timeout := 30 * time.Second
	if t := os.Getenv("QUERY_TIMEOUT"); t != "" {
		parsed, err := time.ParseDuration(t)
		if err != nil {
			log.Fatalf("invalid QUERY_TIMEOUT %q: %v", t, err)
		}
		timeout = parsed
	}

	client := mcp.NewQueryClient(baseURL, timeout, os.Getenv("QUERY_AUTH_TOKEN"))
	server := mcp.NewServer(client)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// MCP_HTTP_ADDR switches the transport: unset means stdio (a local
	// subprocess spawned by the MCP client, today's default), set means a
	// hosted HTTP endpoint that many remote clients share.
	if addr := os.Getenv("MCP_HTTP_ADDR"); addr != "" {
		runHTTP(ctx, server, addr, timeout)
		return
	}

	log.Printf("mcp-server starting (query_service=%s, timeout=%s)", baseURL, timeout)
	if err := server.Run(ctx); err != nil {
		log.Fatal(err)
	}
}

// runHTTP serves the MCP streamable HTTP transport on addr, mirroring
// cmd/query-service's serve/shutdown shape. MCP_AUTH_TOKEN gates access to
// /mcp; it is a separate credential from QUERY_AUTH_TOKEN (which this
// process uses as a *client* against query-service) because the two guard
// different boundaries - one is distributed to every engineer's machine,
// the other never leaves the deployment.
func runHTTP(ctx context.Context, server *mcp.Server, addr string, timeout time.Duration) {
	token := os.Getenv("MCP_AUTH_TOKEN")
	if token == "" {
		// Unlike query-service there is no loopback-default fallback here:
		// the operator explicitly chose HTTP mode, whose entire purpose is
		// shared remote access, so silently rewriting addr would surprise.
		// Warn loudly instead.
		log.Printf("WARNING: MCP_AUTH_TOKEN not set - serving MCP over HTTP without authentication; anyone who can reach %s gets full graph access", addr)
	}

	mux := http.NewServeMux()
	// No method restriction on /mcp: the SDK handler branches on method
	// itself and answers non-POST with a spec-correct 405 + Allow header; a
	// method-restricted pattern would shadow that with the mux's generic 405.
	mux.Handle("/mcp", server.HTTPHandler())
	// Pure liveness for load balancer probes - unauthenticated (WithAuth
	// exempts it), never touches query-service.
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})

	httpServer := &http.Server{
		Addr:              addr,
		Handler:           httpmw.WithAuth(mux, token),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		// Each MCP interaction is one bounded POST (stateless mode rejects
		// SSE), whose slowest leg is the outbound query-service call; give
		// that budget plus margin, same idiom as query-service.
		WriteTimeout: timeout + 10*time.Second,
		IdleTimeout:  120 * time.Second,
	}

	log.Printf("mcp-server listening on %s (auth: %v)", addr, token != "")

	errCh := make(chan error, 1)
	go func() {
		errCh <- httpServer.ListenAndServe()
	}()

	select {
	case err := <-errCh:
		log.Fatal(err)
	case <-ctx.Done():
		log.Printf("shutting down...")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := httpServer.Shutdown(shutdownCtx); err != nil && !errors.Is(err, context.DeadlineExceeded) {
			log.Printf("shutdown: %v", err)
		}
	}
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
