package api

import (
	"encoding/json"
	"net/http"
	"time"

	"a1-knowledge-graph/internal/keys"
)

// EnableKeys mounts POST /keys/validate on the next Routes() call. The
// endpoint is internal: it sits behind QUERY_AUTH_TOKEN like every other
// route, and its only caller is mcp-server translating per-user bearer keys
// into an allow/deny decision. Without a store the route is absent entirely.
func (s *Server) EnableKeys(store *keys.Store) {
	s.keys = store
}

type validateKeyRequest struct {
	Key string `json:"key"`
}

type validateKeyResponse struct {
	Valid     bool      `json:"valid"`
	Owner     string    `json:"owner,omitempty"`
	OwnerName string    `json:"owner_name,omitempty"`
	ExpiresAt time.Time `json:"expires_at,omitzero"`
}

// validateKey answers whether a presented per-user key is currently active.
// Always 200 with a valid flag for a well-formed request - the caller is an
// authenticated internal service, not the key holder, so there is nothing
// to hide in the distinction between absent, revoked, and expired.
func (s *Server) validateKey(w http.ResponseWriter, r *http.Request) {
	var req validateKeyRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body: "+err.Error())
		return
	}
	if !keys.IsUserKey(req.Key) {
		writeJSON(w, http.StatusOK, validateKeyResponse{Valid: false})
		return
	}
	k, ok, err := s.keys.Validate(r.Context(), req.Key)
	if err != nil {
		serverError(w, err)
		return
	}
	if !ok {
		writeJSON(w, http.StatusOK, validateKeyResponse{Valid: false})
		return
	}
	writeJSON(w, http.StatusOK, validateKeyResponse{
		Valid:     true,
		Owner:     k.Owner,
		OwnerName: k.OwnerName,
		ExpiresAt: k.ExpiresAt,
	})
}
