package httpmw

import (
	"context"
	"net/http"
)

// ActorHeader carries a validated end-user identity across the one trusted
// internal hop (mcp-server -> query-service). It is honored ONLY on requests
// that already authenticated with the internal service token, so a public
// caller cannot forge an attribution: reaching query-service at all requires
// the service credential, and mcp-server sets this header only after
// validating the user's own key.
const ActorHeader = "X-A1KG-Actor"

type actorCtxKey struct{}

// WithActor returns ctx carrying actor.
func WithActor(ctx context.Context, actor string) context.Context {
	if actor == "" {
		return ctx
	}
	return context.WithValue(ctx, actorCtxKey{}, actor)
}

// ActorFrom returns the actor carried by ctx, or "" when unattributed.
func ActorFrom(ctx context.Context) string {
	a, _ := ctx.Value(actorCtxKey{}).(string)
	return a
}

// WithForwardedActor reads ActorHeader into the request context. Mount it
// INSIDE the auth middleware so only authenticated callers can set it.
func WithForwardedActor(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if actor := r.Header.Get(ActorHeader); actor != "" {
			r = r.WithContext(WithActor(r.Context(), actor))
		}
		h.ServeHTTP(w, r)
	})
}
