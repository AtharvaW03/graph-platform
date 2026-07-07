package httpapi

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"graph-platform/internal/extract"
)

// extractFragment writes files into a temp repo and returns the raw fragment so
// tests can inspect route metadata and edge confidence (runExtract only exposes
// labels).
func extractFragment(t *testing.T, files map[string]string) *extract.Fragment {
	t.Helper()
	dir := t.TempDir()
	for name, contents := range files {
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(contents), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	frag, err := New().Extract(context.Background(), dir, "test-repo")
	if err != nil {
		t.Fatal(err)
	}
	return frag
}

func routeNodes(frag *extract.Fragment) map[string]extract.FragmentNode {
	m := map[string]extract.FragmentNode{}
	for _, n := range frag.Nodes {
		if n.Type == "http_route" {
			m[n.Label] = n
		}
	}
	return m
}

func edgeConfByTarget(frag *extract.Fragment) map[string]string {
	m := map[string]string{}
	for _, e := range frag.Edges {
		if e.Relation == "exposes_route" {
			m[e.Target] = e.Confidence
		}
	}
	return m
}

// --- unit: parseOpenAPI ---

func TestParseOpenAPIv3YAML(t *testing.T) {
	spec := `openapi: 3.0.1
info:
  title: Funds
  version: "1.0"
servers:
  - url: https://api.example.com/funds/v4
paths:
  /margin:
    post:
      operationId: getMarginDetails
      tags: [margin]
    parameters:
      - name: x
        in: query
  /marginSummary:
    get:
      operationId: getMarginSummary
      tags: [margin, reporting]
`
	routes, err := parseOpenAPI([]byte(spec), "openapi.yaml")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	got := map[string][]string{} // "METHOD path" -> tags
	for _, r := range routes {
		got[r.r.Method+" "+r.r.Path] = r.r.Tags
	}
	if _, ok := got["POST /funds/v4/margin"]; !ok {
		t.Errorf("missing POST /funds/v4/margin; got %v", got)
	}
	if _, ok := got["GET /funds/v4/marginSummary"]; !ok {
		t.Errorf("missing GET /funds/v4/marginSummary; got %v", got)
	}
	// The non-method "parameters" key under /margin must not become a route.
	if len(routes) != 2 {
		t.Errorf("expected 2 routes, got %d: %v", len(routes), got)
	}
	if tags := got["GET /funds/v4/marginSummary"]; len(tags) != 2 {
		t.Errorf("expected 2 tags, got %v", tags)
	}
}

func TestParseSwaggerV2JSON(t *testing.T) {
	spec := `{
  "swagger": "2.0",
  "basePath": "/api/v1",
  "paths": {
    "/users": { "get": { "operationId": "listUsers", "tags": ["users"] } },
    "/users/{id}": { "delete": { "operationId": "deleteUser" } }
  }
}`
	routes, err := parseOpenAPI([]byte(spec), "swagger.json")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	got := map[string]bool{}
	for _, r := range routes {
		got[r.r.Method+" "+r.r.Path] = true
		if r.r.Source != sourceOpenAPI {
			t.Errorf("route %s: source = %q, want openapi", r.r.Path, r.r.Source)
		}
	}
	for _, want := range []string{"GET /api/v1/users", "DELETE /api/v1/users/{id}"} {
		if !got[want] {
			t.Errorf("missing %q; got %v", want, got)
		}
	}
}

func TestParseOpenAPIRejectsNonSpec(t *testing.T) {
	// Valid JSON, but not a spec (no swagger/openapi version key).
	if _, err := parseOpenAPI([]byte(`{"name":"pkg","version":"1.0.0"}`), "package.json"); err == nil {
		t.Error("expected error for non-spec document")
	}
}

func TestIsSpecFile(t *testing.T) {
	yes := []string{"swagger.json", "openapi.yaml", "openapi.yml", "v2-swagger.json", "my-openapi-spec.yaml", "api-docs.json"}
	no := []string{"package.json", "config.yaml", "routes.go", "tsconfig.json", "docker-compose.yml"}
	for _, n := range yes {
		if !isSpecFile(n) {
			t.Errorf("isSpecFile(%q) = false, want true", n)
		}
	}
	for _, n := range no {
		if isSpecFile(n) {
			t.Errorf("isSpecFile(%q) = true, want false", n)
		}
	}
}

func TestClassifyRoute(t *testing.T) {
	cases := map[string]string{
		"/funds/v4/margin": "business",
		"/users":           "business",
		"/metrics":         "infra",
		"/health":          "infra",
		"/debug/pprof/":    "infra",
		"/actuator/info":   "infra",
		"/swagger/index":   "infra",
	}
	for path, want := range cases {
		if got := classifyRoute(path); got != want {
			t.Errorf("classifyRoute(%q) = %q, want %q", path, got, want)
		}
	}
}

// --- integration: spec + code merge ---

func TestOpenAPIMergePrecedenceAndComplement(t *testing.T) {
	frag := extractFragment(t, map[string]string{
		// Spec documents the full versioned business path and one infra route
		// that the code also registers as a literal. No server prefix here so
		// the paths are exactly as written (a server url would prefix them all).
		"docs/openapi.yaml": `openapi: 3.0.0
info: {title: Funds, version: "1.0"}
paths:
  /funds/v4/margin:
    post:
      operationId: getMarginDetails
      tags: [margin]
  /health:
    get:
      operationId: health
`,
		"constants/routes.go": `package constants

const (
	Margin = "margin"
)
`,
		// Code registers the same business route via a cross-file group (path
		// surfaces partial: /margin) and the /health literal (exact overlap).
		"api/router.go": `package api

func Setup(router *gin.Engine) {
	router.GET("/health", healthHandler)
	v4 := router.Group(constants.FundsV4)
	svc.FundsRoutes(v4)
}
`,
		"api/v4/routes.go": `package v4

func FundsRoutes(group *gin.RouterGroup) {
	group.POST(constants.Margin, getMarginDetails)
}
`,
	})

	nodes := routeNodes(frag)
	conf := edgeConfByTarget(frag)

	// 1. Spec route present with full path, EXTRACTED, documented, tagged.
	specNode, ok := nodes["POST /funds/v4/margin"]
	if !ok {
		t.Fatalf("missing spec route POST /funds/v4/margin; got %v", keys(nodes))
	}
	if specNode.Metadata["source"] != sourceOpenAPI || specNode.Metadata["documented"] != true {
		t.Errorf("spec route metadata wrong: %v", specNode.Metadata)
	}
	if conf[specNode.ID] != extract.ConfidenceExtracted {
		t.Errorf("spec route confidence = %q, want EXTRACTED", conf[specNode.ID])
	}

	// 2. Code's partial /margin is NOT dropped (no exact match) - it's the
	//    undocumented-surface signal, kept as INFERRED.
	codeNode, ok := nodes["POST /margin"]
	if !ok {
		t.Fatalf("missing code route POST /margin; got %v", keys(nodes))
	}
	if codeNode.Metadata["source"] != sourceCode {
		t.Errorf("code route source = %v, want code", codeNode.Metadata["source"])
	}
	if conf[codeNode.ID] != extract.ConfidenceInferred {
		t.Errorf("code route confidence = %q, want INFERRED", conf[codeNode.ID])
	}

	// 3. /health is registered by both; spec wins on exact overlap - exactly
	//    one node, sourced from the spec.
	healthCount := 0
	for label := range nodes {
		if label == "GET /health" {
			healthCount++
		}
	}
	if healthCount != 1 {
		t.Errorf("expected exactly 1 GET /health node, got %d", healthCount)
	}
	if nodes["GET /health"].Metadata["source"] != sourceOpenAPI {
		t.Errorf("GET /health should be spec-sourced, got %v", nodes["GET /health"].Metadata["source"])
	}
	if nodes["GET /health"].Metadata["classification"] != "infra" {
		t.Errorf("GET /health classification = %v, want infra", nodes["GET /health"].Metadata["classification"])
	}
}

// TestNoSpecUnchanged confirms a repo without any spec produces only
// code-sourced routes (the always-on baseline is untouched).
func TestNoSpecUnchanged(t *testing.T) {
	frag := extractFragment(t, map[string]string{
		"main.go": `package main

func routes() {
	r.GET("/users", listUsers)
}
`,
	})
	nodes := routeNodes(frag)
	n, ok := nodes["GET /users"]
	if !ok {
		t.Fatalf("missing GET /users; got %v", keys(nodes))
	}
	if n.Metadata["source"] != sourceCode || n.Metadata["documented"] != false {
		t.Errorf("expected code-sourced undocumented route, got %v", n.Metadata)
	}
}

func keys(m map[string]extract.FragmentNode) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
