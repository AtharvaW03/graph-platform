package main

import (
	"context"
	"errors"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"a1-knowledge-graph/internal/admin"
	"a1-knowledge-graph/internal/api"
	"a1-knowledge-graph/internal/control"
	"a1-knowledge-graph/internal/httpmw"
	"a1-knowledge-graph/internal/keys"
	"a1-knowledge-graph/internal/neo4j"
	"a1-knowledge-graph/internal/portal"
	"a1-knowledge-graph/internal/query"
	"a1-knowledge-graph/internal/usage"
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
	// Per-user key validation for mcp-server. Costs nothing when unused;
	// the portal (which mints the keys) is wired below when OIDC is set.
	keyStore := keys.NewStore(client)
	server.EnableKeys(keyStore)

	// Usage attribution feeds the admin dashboard's rankings and the
	// enumeration detector. Best-effort by construction: a full buffer
	// drops samples rather than slowing a query.
	usageRecorder := usage.NewRecorder(client, log.Default())
	defer usageRecorder.Close()
	controlStore := control.NewStore(client)

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

	// Middleware order (outermost first): request logging sees every request
	// including rejected ones; CORS answers preflights before auth runs,
	// since preflights never carry an Authorization header.
	// Inside auth: only authenticated traffic is attributed and recorded,
	// and only an authenticated caller can assert a forwarded actor.
	//
	// WithUsageRecording sits directly around server.Routes() - not outside
	// WithRequestTimeout - because it reads r.PathValue("repo") for
	// path-scoped routes (/overview/{repo}, /dependencies/{repo}), and
	// PathValue is only populated on the exact *http.Request object the
	// pattern-matching mux received. WithRequestTimeout calls
	// r.WithContext(...), which returns a NEW *http.Request; anything
	// reading PathValue from an *outer* layer's r would see it forever
	// unset, since the mux mutates the inner copy, not the original.
	authed := api.WithCORS(
		httpmw.WithAuth(
			httpmw.WithForwardedActor(
				api.WithRequestTimeout(
					api.WithUsageRecording(server.Routes(), usageRecorder),
					requestTimeout,
				),
			),
			token,
		),
		corsOrigin,
	)

	// The key portal (self-service SSO-authenticated key minting) mounts
	// OUTSIDE the bearer-token stack: it authenticates browsers with its own
	// OIDC session, not with QUERY_AUTH_TOKEN. Enabled only when the full
	// OIDC client registration is configured; absent that, /portal/* falls
	// through to the authed stack and 401s like everything else.
	root := http.NewServeMux()
	root.Handle("/", authed)

	var portalHandler *portal.Handler
	oidcCfg := portal.Config{
		Issuer:       os.Getenv("OIDC_ISSUER"),
		ClientID:     os.Getenv("OIDC_CLIENT_ID"),
		ClientSecret: os.Getenv("OIDC_CLIENT_SECRET"),
		RedirectURL:  os.Getenv("OIDC_REDIRECT_URL"),
	}
	if oidcCfg.Enabled() {
		sessionSecret := os.Getenv("PORTAL_SESSION_SECRET")
		if sessionSecret == "" {
			log.Fatal("OIDC_* set but PORTAL_SESSION_SECRET is not - the portal cannot sign sessions")
		}
		auth, err := portal.NewOIDC(context.Background(), oidcCfg)
		if err != nil {
			log.Fatalf("portal: %v", err)
		}
		portalHandler = portal.NewHandler(auth, keyStore, []byte(sessionSecret), strings.HasPrefix(oidcCfg.RedirectURL, "https://"))
		root.Handle("/portal", portalHandler.Routes())
		root.Handle("/portal/", portalHandler.Routes())
		log.Printf("key portal enabled at /portal (issuer %s)", oidcCfg.Issuer)
	}

	// Admin surface: SSO session on the PORTAL_ADMINS allowlist, or a
	// break-glass bearer token. With neither configured it stays unmounted -
	// an admin console must never be reachable by omission.
	adminAuth := admin.NewTokenSessionAuth(
		sessionReaderOrNil(portalHandler),
		os.Getenv("PORTAL_ADMINS"),
		os.Getenv("ADMIN_AUTH_TOKEN"),
	)
	if adminAuth.Enabled() {
		adminHandler := admin.NewHandler(admin.Deps{
			Usage:   usage.NewReader(client),
			Keys:    keyStore,
			Control: controlStore,
			Graph:   api.NewGraphStatsAdapter(svc),
		}, adminAuth, anomalyWindow(), anomalyThreshold())
		root.Handle("/admin", adminHandler.Routes())
		root.Handle("/admin/", adminHandler.Routes())
		log.Printf("admin surface enabled at /admin (%d allowlisted admin(s), token: %v)",
			len(adminAuth.Admins), adminAuth.Token != "")
	} else {
		log.Printf("admin surface disabled (set PORTAL_ADMINS with OIDC, or ADMIN_AUTH_TOKEN)")
	}

	handler := httpmw.WithRequestLog(root, nil)

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

// sessionReaderOrNil avoids handing the admin authorizer a typed-nil
// *portal.Handler, which would satisfy the interface and then panic on use.
func sessionReaderOrNil(p *portal.Handler) admin.SessionReader {
	if p == nil {
		return nil
	}
	return p
}

// anomalyWindow and anomalyThreshold tune enumeration detection: an actor
// touching more than threshold distinct repositories inside the window is
// surfaced. Defaults suit ~100 engineers on a few dozen repositories -
// normal work touches a handful, a sweep touches everything.
func anomalyWindow() time.Duration {
	if v := os.Getenv("ADMIN_ANOMALY_WINDOW"); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			return d
		}
		log.Printf("WARNING: ADMIN_ANOMALY_WINDOW %q is not a duration, using default", v)
	}
	return time.Hour
}

func anomalyThreshold() int {
	if v := os.Getenv("ADMIN_ANOMALY_THRESHOLD"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
		log.Printf("WARNING: ADMIN_ANOMALY_THRESHOLD %q is not a positive integer, using default", v)
	}
	return 10
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
