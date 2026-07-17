package httpapi

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestSwaggerV2JSONIngestion: a swagger.json (Swagger 2.0, JSON, basePath
// prefix) must contribute documented routes even when the code never
// declares them with matchable syntax.
func TestSwaggerV2JSONIngestion(t *testing.T) {
	frag := runExtractFrag(t, map[string]string{
		"docs/swagger.json": `{
  "swagger": "2.0",
  "info": {"title": "us-funds", "version": "1.0"},
  "basePath": "/v1",
  "paths": {
    "/deposit/verify-otp": {
      "post": {"operationId": "VerifyOTP", "tags": ["deposit"]}
    },
    "/fx/{ccy}": {
      "get": {"operationId": "GetFX"}
    }
  }
}`,
	})
	routes := map[string]bool{}
	documented := map[string]bool{}
	for _, n := range frag.Nodes {
		if n.Type == "http_route" {
			routes[n.Label] = true
			if d, _ := n.Metadata["documented"].(bool); d {
				documented[n.Label] = true
			}
		}
	}
	for _, want := range []string{"POST /v1/deposit/verify-otp", "GET /v1/fx/{ccy}"} {
		if !routes[want] {
			t.Errorf("missing spec route %q; got %v", want, routes)
		}
		if !documented[want] {
			t.Errorf("spec route %q not marked documented", want)
		}
	}
}

// TestOpenAPIV3YAMLIngestion: an openapi.yaml (v3, servers-based prefix).
func TestOpenAPIV3YAMLIngestion(t *testing.T) {
	routes := runExtract(t, map[string]string{
		"openapi.yaml": `openapi: 3.0.1
servers:
  - url: https://api.example.com/v2
paths:
  /wallet/balance:
    get:
      operationId: getWalletBalance
`,
	})
	if !routes["GET /v2/wallet/balance"] {
		t.Fatalf("v3 spec route missing; got %v", routes)
	}
}

// TestSpecReconciliation: on an exact (method, path) overlap the spec route
// wins (documented: true), and code-only routes are still kept.
func TestSpecReconciliation(t *testing.T) {
	frag := runExtractFrag(t, map[string]string{
		"swagger.json": `{"swagger":"2.0","paths":{"/users":{"get":{"operationId":"listUsers"}}}}`,
		"main.go": `package main

func routes() {
	router.GET("/users", listUsers)
	router.POST("/undocumented", secretHandler)
}
`,
	})
	byLabel := map[string]map[string]any{}
	for _, n := range frag.Nodes {
		if n.Type == "http_route" {
			byLabel[n.Label] = n.Metadata
		}
	}
	if len(byLabel) != 2 {
		t.Fatalf("got %d routes, want 2 (spec/code overlap must merge): %v", len(byLabel), byLabel)
	}
	if d, _ := byLabel["GET /users"]["documented"].(bool); !d {
		t.Errorf("overlapping route should be the documented spec version: %v", byLabel["GET /users"])
	}
	if _, ok := byLabel["POST /undocumented"]; !ok {
		t.Errorf("code-only route suppressed by spec reconciliation: %v", byLabel)
	}
}

// TestSpecReconciliation_ParamSyntax: gin's `:id`, OpenAPI's `{id}`, and
// Flask's `<id>` spellings of the same parameterized endpoint must reconcile
// to ONE route, not inflate the inventory with a twin per spelling.
func TestSpecReconciliation_ParamSyntax(t *testing.T) {
	frag := runExtractFrag(t, map[string]string{
		"swagger.json": `{"swagger":"2.0","paths":{
			"/v1/transactions/{transactionId}/status":{"get":{"operationId":"txStatus"}},
			"/v1/fx/{ccy}":{"get":{"operationId":"getFX"}}
		}}`,
		"main.go": `package main

func routes() {
	router.GET("/v1/transactions/:transactionId/status", statusHandler)
	router.GET("/v1/fx/:ccy", fxHandler)
}
`,
	})
	var routeLabels []string
	documented := 0
	for _, n := range frag.Nodes {
		if n.Type == "http_route" {
			routeLabels = append(routeLabels, n.Label)
			if d, _ := n.Metadata["documented"].(bool); d {
				documented++
			}
		}
	}
	if len(routeLabels) != 2 {
		t.Fatalf("got %d routes, want 2 (param-syntax twins must merge): %v", len(routeLabels), routeLabels)
	}
	if documented != 2 {
		t.Fatalf("spec version should win the merge (documented=true), got %d documented", documented)
	}
}

// TestTestFilesExcluded: routes registered in test files are not API surface.
func TestTestFilesExcluded(t *testing.T) {
	routes := runExtract(t, map[string]string{
		"api/routes.go":      "package api\n\nfunc r() { router.GET(\"/live\", h) }\n",
		"api/routes_test.go":  "package api\n\nfunc t() { router.GET(\"/test-only\", h) }\n",
		"web/app.test.ts":     "app.get('/ts-test-only', h);\n",
		"testdata/fixture.go": "package fixture\n\nfunc f() { router.GET(\"/fixture-only\", h) }\n",
	})
	if !routes["GET /live"] {
		t.Fatalf("live route missing: %v", routes)
	}
	for label := range routes {
		if label != "GET /live" {
			t.Errorf("test code produced route %q", label)
		}
	}
}

// TestSpecSizeCapIsLoud: a spec over the cap must produce a fragment warning,
// never a silent skip - a skipped spec means documented routes are missing
// from the graph.
func TestSpecSizeCapIsLoud(t *testing.T) {
	dir := t.TempDir()
	spec := `{"swagger":"2.0","paths":{"/big":{"get":{}}}}`
	if err := os.WriteFile(filepath.Join(dir, "swagger.json"), []byte(spec), 0o644); err != nil {
		t.Fatal(err)
	}
	e := New()
	e.SpecMaxBytes = 8 // force the cap below the file size
	frag, err := e.Extract(context.Background(), dir, "test-repo")
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range frag.Nodes {
		if n.Type == "http_route" {
			t.Fatalf("over-cap spec still produced route %q", n.Label)
		}
	}
	for _, w := range frag.Warnings {
		if strings.Contains(w, "swagger.json") && strings.Contains(w, "exceeds") {
			return
		}
	}
	t.Fatalf("over-cap spec skipped without a warning: %v", frag.Warnings)
}

// TestSpecParseFailureIsLoud: a malformed spec must warn, not vanish.
func TestSpecParseFailureIsLoud(t *testing.T) {
	frag := runExtractFrag(t, map[string]string{
		"openapi.yaml": "openapi: 3.0.1\npaths: [this is not a paths map",
	})
	for _, w := range frag.Warnings {
		if strings.Contains(w, "openapi.yaml") {
			return
		}
	}
	t.Fatalf("malformed spec produced no warning: %v", frag.Warnings)
}
