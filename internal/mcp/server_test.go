package mcp

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"a1-knowledge-graph/internal/httpmw"
)

// bearerTransport injects a static Authorization header on every request,
// standing in for Claude Code's --header "Authorization: Bearer ..." config.
type bearerTransport struct {
	token string
	base  http.RoundTripper
}

func (t *bearerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req = req.Clone(req.Context())
	req.Header.Set("Authorization", "Bearer "+t.token)
	return t.base.RoundTrip(req)
}

// newTestStack wires the full hosted-mode stack in-process: a stub
// query-service, a QueryClient against it, the MCP server's HTTP handler
// mounted at /mcp behind WithAuth - the same shape cmd/mcp-server's HTTP
// mode serves.
func newTestStack(t *testing.T, token string) *httptest.Server {
	t.Helper()
	queryStub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/search" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"name":"processPayment()","repo":"payments-service"}]`))
	}))
	t.Cleanup(queryStub.Close)

	server := NewServer(NewQueryClient(queryStub.URL, 5*time.Second, ""))
	mux := http.NewServeMux()
	mux.Handle("/mcp", server.HTTPHandler())
	ts := httptest.NewServer(httpmw.WithAuth(mux, token))
	t.Cleanup(ts.Close)
	return ts
}

// TestHTTPHandler_ToolCallRoundTrip drives the SDK's own client over the
// streamable HTTP transport against HTTPHandler - initialize, then a real
// search_code call - asserting on the proxied query-service payload, not
// just "a response came back".
func TestHTTPHandler_ToolCallRoundTrip(t *testing.T) {
	const token = "test-mcp-token"
	ts := newTestStack(t, token)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	client := sdk.NewClient(&sdk.Implementation{Name: "test-client", Version: "0.0.1"}, nil)
	session, err := client.Connect(ctx, &sdk.StreamableClientTransport{
		Endpoint: ts.URL + "/mcp",
		HTTPClient: &http.Client{
			Transport: &bearerTransport{token: token, base: http.DefaultTransport},
		},
		// Stateless servers reject the optional standalone GET/SSE stream
		// with 405; skip it rather than exercising the client's retry path.
		DisableStandaloneSSE: true,
	}, nil)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer session.Close()

	res, err := session.CallTool(ctx, &sdk.CallToolParams{
		Name:      "search_code",
		Arguments: map[string]any{"query": "processPayment"},
	})
	if err != nil {
		t.Fatalf("call search_code: %v", err)
	}
	if res.IsError {
		t.Fatalf("search_code returned tool error: %+v", res.Content)
	}
	if len(res.Content) != 1 {
		t.Fatalf("content items = %d, want 1", len(res.Content))
	}
	text, ok := res.Content[0].(*sdk.TextContent)
	if !ok {
		t.Fatalf("content type = %T, want *sdk.TextContent", res.Content[0])
	}
	if !strings.Contains(text.Text, "processPayment()") {
		t.Fatalf("tool result %q does not contain the stubbed symbol", text.Text)
	}
}

// TestHTTPHandler_AuthBeforeProtocolValidation: a request with a bad token
// AND malformed MCP headers must come back 401 (auth), not 400 (the SDK's
// own Content-Type/Accept validation) - an unauthenticated caller learns
// nothing about the expected request shape.
func TestHTTPHandler_AuthBeforeProtocolValidation(t *testing.T) {
	ts := newTestStack(t, "test-mcp-token")

	req, err := http.NewRequest(http.MethodPost, ts.URL+"/mcp", strings.NewReader("not json"))
	if err != nil {
		t.Fatal(err)
	}
	// Deliberately no Authorization, no Content-Type, no Accept.
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d (%s), want 401", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	// With the right token the same malformed request must now surface the
	// SDK's own validation instead - proving auth sits in front, not behind.
	req2, err := http.NewRequest(http.MethodPost, ts.URL+"/mcp", strings.NewReader("not json"))
	if err != nil {
		t.Fatal(err)
	}
	req2.Header.Set("Authorization", "Bearer test-mcp-token")
	resp2, err := http.DefaultClient.Do(req2)
	if err != nil {
		t.Fatal(err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode == http.StatusUnauthorized {
		t.Fatalf("authenticated request still rejected as 401")
	}
	if resp2.StatusCode < 400 || resp2.StatusCode >= 500 {
		t.Fatalf("malformed authenticated request: status = %d, want a 4xx from SDK validation", resp2.StatusCode)
	}
}
