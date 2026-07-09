// Package deps extracts repository dependencies across the supported
// ecosystems. Each parser consumes one manifest file and emits Dep entries; the
// Extractor walks the repo, dispatches by manifest filename, and produces a
// fragment of Package nodes, DEPENDS_ON edges, and DEPENDS_ON_REPO edges when a
// dependency matches a configured internal-org prefix.
package deps

import (
	"strings"
)

// Dep is one normalized dependency extracted from any manifest format.
type Dep struct {
	Name      string // canonical package identifier
	Version   string // optional; empty if the manifest declared none
	Ecosystem string // go | npm | pypi | maven | gradle | sbt | cargo | nuget | composer | rubygems | swiftpm | cmake | conan | vcpkg
	Scope     string // optional: runtime | dev | test | build; empty for unspecified
	Manifest  string // path to the manifest file the dep was found in (repo-relative)
}

// PackageNodeID is the fragment-node ID for a package. The ecosystem is folded
// in so a Go and an npm package with the same short name don't collide.
func PackageNodeID(d Dep) string {
	return "pkg::" + d.Ecosystem + "::" + safeID(d.Name)
}

// safeID makes a string safe inside a node ID: no spaces or "..", lowercased so
// casing variations (org/Repo vs org/repo) collapse to one node.
func safeID(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.ReplaceAll(s, " ", "_")
	s = strings.ReplaceAll(s, "..", "_")
	return s
}

// InternalRepoNameFromDep returns the internal repo name for a dep matching one
// of the org prefixes (e.g. "github.com/example-org/auth-service" ->
// "auth-service"), or "" if none match. Prefixes should include the trailing
// slash; the first match wins.
func InternalRepoNameFromDep(name string, orgPrefixes []string) string {
	for _, prefix := range orgPrefixes {
		if prefix == "" {
			continue
		}
		if strings.HasPrefix(name, prefix) {
			tail := strings.TrimPrefix(name, prefix)
			// Strip trailing "/v2"-style Go module suffixes and version tags.
			if i := strings.Index(tail, "/"); i >= 0 {
				tail = tail[:i]
			}
			return tail
		}
	}
	return ""
}
