package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
)

// ValidateKey asks query-service whether a per-user key is currently active.
// Used by the HTTP-mode auth middleware; not an MCP tool. Any transport or
// server error is returned as err (fail closed at the caller), never as a
// silent "invalid".
func (c *QueryClient) ValidateKey(ctx context.Context, key string) (owner string, valid bool, err error) {
	body, err := json.Marshal(map[string]string{"key": key})
	if err != nil {
		return "", false, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/keys/validate", bytes.NewReader(body))
	if err != nil {
		return "", false, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return "", false, fmt.Errorf("call query service: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", false, fmt.Errorf("keys/validate: status %d", resp.StatusCode)
	}
	var out struct {
		Valid bool   `json:"valid"`
		Owner string `json:"owner"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", false, fmt.Errorf("decode response: %w", err)
	}
	return out.Owner, out.Valid, nil
}
