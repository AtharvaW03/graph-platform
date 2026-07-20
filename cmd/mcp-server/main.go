package main

import (
	"context"
	"errors"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
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
		// An unauthenticated HTTP endpoint serves the whole graph to anyone
		// who can reach it. Loopback is fine (local dev); a reachable
		// address requires an explicit acknowledgment rather than a silent
		// warning, so a misconfigured secret fails closed instead of
		// exposing the graph.
		allowInsecure := os.Getenv("MCP_ALLOW_NO_AUTH") == "1" || strings.EqualFold(os.Getenv("MCP_ALLOW_NO_AUTH"), "true")
		switch {
		case isLoopbackAddr(addr):
			log.Printf("WARNING: MCP_AUTH_TOKEN not set - serving MCP unauthenticated on loopback %s (local use only)", addr)
		case allowInsecure:
			log.Printf("WARNING: MCP_AUTH_TOKEN not set and MCP_ALLOW_NO_AUTH is set - serving MCP unauthenticated on %s; anyone who can reach it gets full graph access", addr)
		default:
			log.Fatalf("MCP_AUTH_TOKEN not set and %s is not loopback: refusing to serve the graph unauthenticated on a reachable address. Set MCP_AUTH_TOKEN, or set MCP_ALLOW_NO_AUTH=1 to override (only behind a trusted boundary that authenticates for you).", addr)
		}
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
		Handler:           httpmw.WithRequestLog(httpmw.WithAuth(mux, token), nil),
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

// isLoopbackAddr reports whether a listen address binds only the loopback
// interface. A bare port (":8090") or 0.0.0.0 binds all interfaces and is
// not loopback.
func isLoopbackAddr(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		host = addr
	}
	if host == "" {
		return false
	}
	if host == "localhost" {
		return true
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback()
	}
	return false
}
