package httpapi

import (
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// OpenAPI / Swagger spec ingestion.
//
// A committed OpenAPI (v3) or Swagger (v2) document is the authored contract
// for a service's HTTP surface: it lists documented endpoints with their exact
// full paths, methods, and tags - no heuristics. Where the source-scanning
// matchers infer routes (and can miss cross-file prefixes or emit partial
// paths), a spec is ground truth. So when a repo ships one, we parse it and
// reconcile it with the code-derived routes (see reconcileRoutes): spec routes
// carry ConfidenceExtracted; code routes stay ConfidenceInferred.
//
// This never replaces the code path. Specs omit undocumented and infra
// surface (health/metrics/pprof), which only source scanning finds, so the two
// are complementary. A repo without a spec is unaffected - the code path is
// the always-on baseline.

// oasDoc is the minimal slice of an OpenAPI/Swagger document we read. `paths`
// values are decoded lazily per key: a path-item object mixes HTTP-method keys
// (get/post/...) with non-method keys (parameters, $ref, servers, summary), so
// each entry is a raw yaml.Node we decode into oasOperation only for the keys
// that name a method.
type oasDoc struct {
	Swagger  string                          `yaml:"swagger"`  // "2.0" for Swagger v2
	OpenAPI  string                          `yaml:"openapi"`  // "3.x.x" for OpenAPI v3
	BasePath string                          `yaml:"basePath"` // Swagger v2 global prefix
	Servers  []oasServer                     `yaml:"servers"`  // OpenAPI v3 server list
	Paths    map[string]map[string]yaml.Node `yaml:"paths"`
}

type oasServer struct {
	URL string `yaml:"url"`
}

type oasOperation struct {
	Tags        []string `yaml:"tags"`
	OperationID string   `yaml:"operationId"`
}

// oasMethods are the path-item keys that name an HTTP operation. Everything
// else in a path item (parameters, $ref, servers, summary, description) is
// skipped.
var oasMethods = map[string]bool{
	"get": true, "post": true, "put": true, "delete": true,
	"patch": true, "head": true, "options": true, "trace": true,
}

// openAPIRoutes discovers and parses every OpenAPI/Swagger document under
// repoPath and returns the documented routes as heldRoutes tagged source
// "openapi". Parse failures are surfaced as fragment warnings, never fatal:
// a malformed spec must not sink the code-derived routes.
func (e *Extractor) openAPIRoutes(repoPath string, frag interface{ Warn(string) }) []heldRoute {
	maxBytes := e.MaxFileBytes
	if maxBytes <= 0 {
		maxBytes = 2 * 1024 * 1024
	}

	var out []heldRoute
	_ = filepath.WalkDir(repoPath, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if shouldSkipDir(d.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if !isSpecFile(d.Name()) {
			return nil
		}
		info, statErr := d.Info()
		if statErr != nil || info.Size() > maxBytes {
			return nil
		}
		rel, _ := filepath.Rel(repoPath, path)
		rel = filepath.ToSlash(rel)

		data, rerr := os.ReadFile(path)
		if rerr != nil {
			return nil
		}
		routes, perr := parseOpenAPI(data, rel)
		if perr != nil {
			frag.Warn(fmt.Sprintf("%s: openapi parse: %v", rel, perr))
			return nil
		}
		out = append(out, routes...)
		return nil
	})
	return out
}

// isSpecFile is a cheap filename prefilter. It intentionally over-matches
// (any *.json/*.yaml/*.yml whose name mentions swagger/openapi, plus the
// conventional exact names); parseOpenAPI then rejects anything that isn't
// actually a spec (no swagger/openapi version key, or no paths), so a false
// positive here costs one parse attempt, not a bogus route.
func isSpecFile(name string) bool {
	l := strings.ToLower(name)
	if !strings.HasSuffix(l, ".json") && !strings.HasSuffix(l, ".yaml") && !strings.HasSuffix(l, ".yml") {
		return false
	}
	switch l {
	case "swagger.json", "openapi.json", "swagger.yaml", "openapi.yaml",
		"swagger.yml", "openapi.yml", "api-docs.json", "apidocs.json":
		return true
	}
	return strings.Contains(l, "swagger") || strings.Contains(l, "openapi")
}

// parseOpenAPI decodes one spec document (JSON or YAML - yaml.v3 parses both)
// and returns its documented routes. It returns an error only when the file
// parses but is not a usable spec, so callers can warn; a nil slice with nil
// error means a valid spec with no paths.
func parseOpenAPI(data []byte, specFile string) ([]heldRoute, error) {
	var doc oasDoc
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, err
	}
	if doc.Swagger == "" && doc.OpenAPI == "" {
		return nil, fmt.Errorf("not an openapi/swagger document (no version key)")
	}
	prefix := specPrefix(&doc)

	var out []heldRoute
	for rawPath, item := range doc.Paths {
		full := joinPrefix(prefix, rawPath)
		for method, node := range item {
			if !oasMethods[strings.ToLower(method)] {
				continue
			}
			var op oasOperation
			_ = node.Decode(&op) // best-effort; tags/operationId are optional
			out = append(out, heldRoute{
				file: specFile,
				r: route{
					Method:  strings.ToUpper(method),
					Path:    full,
					Handler: op.OperationID,
					Source:  sourceOpenAPI,
					Tags:    op.Tags,
				},
			})
		}
	}
	return out, nil
}

// specPrefix returns the global path prefix a spec applies to every route:
// Swagger v2's basePath, or the path component of OpenAPI v3's first server
// URL. Server URLs may be absolute ("https://api.example.com/v2") or relative
// ("/v2"); only the path is used. Empty when the spec defines no prefix.
func specPrefix(doc *oasDoc) string {
	if doc.BasePath != "" {
		return doc.BasePath
	}
	for _, s := range doc.Servers {
		if s.URL == "" {
			continue
		}
		if u, err := url.Parse(s.URL); err == nil {
			if p := strings.TrimRight(u.Path, "/"); p != "" {
				return p
			}
		} else if strings.HasPrefix(s.URL, "/") {
			return strings.TrimRight(s.URL, "/")
		}
	}
	return ""
}
