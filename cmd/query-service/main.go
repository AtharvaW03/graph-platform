package main

import (
	"context"
	"errors"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"graph-platform/internal/api"
	"graph-platform/internal/httpmw"
	"graph-platform/internal/neo4j"
	"graph-platform/internal/query"
)

// requestTimeout bounds each request end-to-end, propagating through the
// request context into the Cypher transaction. It's slightly longer than the
// query layer's own transaction timeout so the DB-side limit fires first.
const requestTimeout = 35 * time.Second

func main() {
	password := os.Getenv("NEO4J_PASSWORD")
	if password == "" {
		log.Fatal("NEO4J_PASSWORD not set")
	}

	uri := envOr("NEO4J_URI", "neo4j://127.0.0.1:7687")
	user := envOr("NEO4J_USER", "neo4j")
	port := envOr("QUERY_PORT", "8080")

	client, err := neo4j.New(uri, user, password)
	if err != nil {
		log.Fatalf("neo4j connect: %v", err)
	}
	defer client.Close()

	svc := query.NewService(client)
	server := api.NewServer(svc, client)

	token := os.Getenv("QUERY_AUTH_TOKEN")

	// No token means no auth, so default to loopback-only rather than exposing
	// an unauthenticated service to the network. QUERY_BIND overrides either way.
	bind := os.Getenv("QUERY_BIND")
	if bind == "" {
		if token == "" {
			bind = "127.0.0.1"
			log.Printf("WARNING: QUERY_AUTH_TOKEN not set - serving without authentication, bound to 127.0.0.1 only (set QUERY_BIND to override)")
		} else {
			log.Printf("auth enabled, listening on all interfaces")
		}
	}

	// An operator who sets QUERY_BIND directly can still end up unauthenticated
	// on a non-loopback address (the default-to-127.0.0.1 above only covers the
	// unset case). That's a legitimate setup behind a network boundary - an
	// internal ALB doing its own auth, say - so this warns rather than
	// refusing to start, but it has to be loud: it's easy to reach by setting
	// QUERY_BIND without also setting QUERY_AUTH_TOKEN.
	if token == "" && !isLoopbackBind(bind) {
		log.Printf("WARNING: serving unauthenticated on %s; anyone with network access can read the graph", net.JoinHostPort(bind, port))
	}

	corsOrigin := os.Getenv("QUERY_CORS_ORIGIN")
	if corsOrigin != "" {
		log.Printf("CORS enabled for origin %s", corsOrigin)
	}

	// Middleware order (outermost first): CORS answers preflights before auth
	// runs, since preflights never carry an Authorization header.
	handler := api.WithCORS(
		httpmw.WithAuth(
			api.WithRequestTimeout(server.Routes(), requestTimeout),
			token,
		),
		corsOrigin,
	)

	addr := net.JoinHostPort(bind, port)
	log.Printf("query-service listening on %s (auth: %v)", addr, token != "")

	httpServer := &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      requestTimeout + 10*time.Second,
		IdleTimeout:       120 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

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

// isLoopbackBind reports whether host refers to this machine only. An empty
// host means "all interfaces" in net.Listen - never loopback.
func isLoopbackBind(host string) bool {
	if host == "" {
		return false
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
