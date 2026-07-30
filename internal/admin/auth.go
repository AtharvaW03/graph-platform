package admin

import (
	"crypto/subtle"
	"net/http"
	"strings"
)

// SessionReader resolves a browser session to an identity.
// *portal.Handler satisfies this.
type SessionReader interface {
	SessionEmail(r *http.Request) (string, bool)
}

// TokenSessionAuth authorizes admins two ways:
//
//   - a portal SSO session whose email is on the allowlist (the normal
//     path: a real person, named in the audit trail), or
//   - a static bearer token (for scripts and break-glass access, audited
//     as "token-admin" since it carries no identity).
//
// Both disabled means the admin surface is unreachable, which is the right
// default: it must never be open by omission.
type TokenSessionAuth struct {
	Sessions SessionReader
	Admins   map[string]bool
	Token    string
}

// NewTokenSessionAuth builds an Authorizer. admins is a comma-separated
// allowlist of email addresses; matching is case-insensitive.
func NewTokenSessionAuth(sessions SessionReader, admins, token string) *TokenSessionAuth {
	set := make(map[string]bool)
	for _, a := range strings.Split(admins, ",") {
		if a = strings.ToLower(strings.TrimSpace(a)); a != "" {
			set[a] = true
		}
	}
	return &TokenSessionAuth{Sessions: sessions, Admins: set, Token: token}
}

// Enabled reports whether any admin path is configured.
func (a *TokenSessionAuth) Enabled() bool {
	return a != nil && (len(a.Admins) > 0 && a.Sessions != nil || a.Token != "")
}

// Authorize implements Authorizer.
func (a *TokenSessionAuth) Authorize(r *http.Request) (string, bool) {
	if a == nil {
		return "", false
	}
	// Bearer token first: it needs no session lookup.
	if a.Token != "" {
		if got, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer "); ok {
			if subtle.ConstantTimeCompare([]byte(got), []byte(a.Token)) == 1 {
				return "token-admin", true
			}
		}
	}
	if a.Sessions != nil && len(a.Admins) > 0 {
		if email, ok := a.Sessions.SessionEmail(r); ok {
			if a.Admins[strings.ToLower(strings.TrimSpace(email))] {
				return email, true
			}
		}
	}
	return "", false
}
