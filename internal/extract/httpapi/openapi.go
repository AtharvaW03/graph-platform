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

// OpenAPI / Swagger spec ingestion. A committed spec is the authored contract
// for a service's HTTP surface (exact paths, methods, tags), so spec routes are
// treated as ConfidenceExtracted and reconciled with the inferred code routes.
// Specs omit undocumented/infra endpoints, so they complement rather than
// replace source scanning.

// oasDoc is the slice of an OpenAPI/Swagger document we read. A path-item mixes
// HTTP-method keys with non-method keys (parameters, $ref, servers, ...), so
// each value stays a raw yaml.Node we decode only for method keys.
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

// oasMethods are the path-item keys that name an HTTP operation.
var oasMethods = map[string]bool{
	"get": true, "post": true, "put": true, "delete": true,
	"patch": true, "head": true, "options": true, "trace": true,
}

// openAPIRoutes parses every OpenAPI/Swagger document under repoPath and
// returns the documented routes. Parse failures become warnings, not errors,
// so a malformed spec can't sink the code-derived routes.
func (e *Extractor) openAPIRoutes(repoPath string, frag interface{ Warn(string) }) []heldRoute {
	maxBytes := e.SpecMaxBytes
	if maxBytes <= 0 {
		maxBytes = 10 * 1024 * 1024
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
		rel, _ := filepath.Rel(repoPath, path)
		rel = filepath.ToSlash(rel)

		// A spec is the authored route contract - skipping one silently
		// leaves documented routes out of the graph with no trace, so every
		// skip below is a warning.
		info, statErr := d.Info()
		if statErr != nil {
			frag.Warn(fmt.Sprintf("%s: openapi spec skipped: stat: %v", rel, statErr))
			return nil
		}
		if info.Size() > maxBytes {
			frag.Warn(fmt.Sprintf("%s: openapi spec skipped: %d bytes exceeds the %d-byte spec cap; its documented routes are missing from the graph", rel, info.Size(), maxBytes))
			return nil
		}

		data, rerr := os.ReadFile(path)
		if rerr != nil {
			frag.Warn(fmt.Sprintf("%s: openapi spec skipped: read: %v", rel, rerr))
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

// isSpecFile is a cheap filename prefilter. It over-matches on purpose;
// parseOpenAPI rejects non-specs, so a false positive costs one parse attempt.
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

// parseOpenAPI decodes one spec (yaml.v3 parses both JSON and YAML) and
// returns its routes. It errors only when the file parses but isn't a usable
// spec; a nil slice with nil error means a valid spec with no paths.
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
// URL. Empty when the spec defines no prefix.
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
