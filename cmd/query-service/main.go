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
	"graph-platform/internal/neo4j"
	"graph-platform/internal/query"
)

// requestTimeout bounds each request end-to-end; it propagates through the
// request context into the Cypher transaction. Slightly longer than the
// query layer's own transaction timeout so the DB-side limit fires first
// and surfaces as a query error rather than a dropped connection.
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
	server := api.NewServer(svc)

	token := os.Getenv("QUERY_AUTH_TOKEN")

	// Without a token there is no authentication, so default to loopback-only:
	// open mode should mean "open to this machine", not "open to the network".
	// QUERY_BIND overrides in both directions for operators who know better.
	bind := os.Getenv("QUERY_BIND")
	if bind == "" {
		if token == "" {
			bind = "127.0.0.1"
			log.Printf("WARNING: QUERY_AUTH_TOKEN not set - serving without authentication, bound to 127.0.0.1 only (set QUERY_BIND to override)")
		} else {
			log.Printf("auth enabled, listening on all interfaces")
		}
	}

	corsOrigin := os.Getenv("QUERY_CORS_ORIGIN")
	if corsOrigin != "" {
		log.Printf("CORS enabled for origin %s", corsOrigin)
	}

	// Middleware order (outermost first): CORS answers preflights before auth
	// (preflights never carry Authorization), auth gates everything else, the
	// timeout scopes the handler work.
	handler := api.WithCORS(
		api.WithAuth(
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
