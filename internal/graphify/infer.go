package graphify

import (
	"crypto/sha1"
	"encoding/hex"
	"regexp"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

var fileExtRe = regexp.MustCompile(`\.(go|kt|sh|py|md|ya?ml|json|toml)$`)

// typeToLabel maps a node's `type` field to a Neo4j label. Extractors set
// node.type to one of these keys to control how their entities are labelled.
var typeToLabel = map[string]string{
	"package":        "Package",
	"dependency":     "Package",
	"http_route":     "HttpRoute",
	"kafka_topic":    "KafkaTopic",
	"kafka_producer": "KafkaProducer",
	"kafka_consumer": "KafkaConsumer",
	"sql_schema":     "SqlSchema",
	"sql_table":      "SqlTable",
	"sql_view":       "SqlView",
	"sql_procedure":  "SqlProcedure",
	"sql_trigger":    "SqlTrigger",
	"sql_function":   "SqlFunction",
	"glue_job":       "GlueJob",
	"glue_schedule":  "GlueSchedule",
}

// InferLabel returns the Neo4j label for a node, first-match-wins. An explicit
// `type` takes priority over the filename/label heuristics below.
func InferLabel(n Node) string {
	if l, ok := typeToLabel[n.Type]; ok {
		return l
	}

	switch n.MetaString("kind") {
	case "file", "bash_entrypoint":
		return "File"
	case "bash_function":
		return "Function"
	}

	sf := strings.ToLower(n.SourceFile)
	if strings.HasSuffix(sf, ".md") || strings.HasSuffix(sf, ".mdx") || strings.HasSuffix(sf, ".rst") {
		return "DocSection"
	}

	if fileExtRe.MatchString(n.Label) {
		return "File"
	}

	if strings.HasSuffix(n.Label, "()") {
		return "Function"
	}

	if r, _ := utf8.DecodeRuneInString(n.Label); r != utf8.RuneError &&
		unicode.IsUpper(r) &&
		!strings.Contains(n.Label, "(") &&
		!strings.Contains(n.Label, " ") {
		return "Class"
	}

	return "Symbol"
}

// relationMap maps relation verbs to Neo4j relationship types. It doubles as
// the allowlist: ImportLinks skips any relation not listed here.
var relationMap = map[string]string{
	// graphify built-in code relations
	"calls":      "CALLS",
	"contains":   "CONTAINS",
	"references": "REFERENCES",
	"method":     "HAS_METHOD",
	"embeds":     "EMBEDS",
	"defines":    "DECLARES",

	// repository dependency extractor
	"depends_on":      "DEPENDS_ON",
	"depends_on_repo": "DEPENDS_ON_REPO",

	// http api extractor
	"exposes_route": "EXPOSES_ROUTE",
	"handled_by":    "HANDLED_BY",

	// kafka extractor
	"produces": "PRODUCES",
	"consumes": "CONSUMES",

	// sql server extractor
	"reads_table":       "READS_TABLE",
	"writes_table":      "WRITES_TABLE",
	"triggers_on":       "TRIGGERS_ON",
	"depends_on_object": "DEPENDS_ON_OBJECT",
	"in_schema":         "IN_SCHEMA",

	// aws glue extractor
	"reads_source":       "READS_SOURCE",
	"writes_destination": "WRITES_DESTINATION",
	"scheduled":          "SCHEDULED",
}

// MapRelation maps a Graphify relation string to a Neo4j relationship type.
// Returns ("", false) for unknown relations - callers should skip those edges.
func MapRelation(relation string) (string, bool) {
	r, ok := relationMap[relation]
	return r, ok
}

// allRelationTypes is every Neo4j relationship type ImportLinks can write,
// computed once. It deliberately does NOT include HAS_ENTITY - that's the
// platform's own repo-ownership edge (internal/neo4j importNodeBatch), never
// a value in relationMap, so it's excluded from this list automatically
// rather than by filtering.
var allRelationTypes = sortedRelationValues()

func sortedRelationValues() []string {
	seen := make(map[string]bool, len(relationMap))
	out := make([]string, 0, len(relationMap))
	for _, v := range relationMap {
		if seen[v] {
			continue
		}
		seen[v] = true
		out = append(out, v)
	}
	sort.Strings(out)
	return out
}

// AllRelationTypes returns every Neo4j relationship type graphify/extractors
// can produce, sorted. Callers that need a traversal allowlist (e.g. a
// shortestPath query that must not route through the Repository hub) use
// this instead of hand-maintaining a duplicate list.
func AllRelationTypes() []string {
	return allRelationTypes
}

// sharedIDPrefixes mark org-global entities - a topic, package, SQL object, or
// repo hub is one node shared across every repo that references it, so
// cross-repo queries stay single-hop. Shared nodes carry shared=true, no repo
// property, and are skipped by the repo-scoped sweep.
var sharedIDPrefixes = []string{"topic::", "pkg::", "sql::", "repo::"}

// IsShared reports whether a platform-emitted node is an org-global entity.
func IsShared(n Node) bool {
	if n.Origin != "platform" {
		return false
	}
	for _, p := range sharedIDPrefixes {
		if strings.HasPrefix(n.ID, p) {
			return true
		}
	}
	return false
}

// StableKey returns the Neo4j node_key for a node.
//
// Platform-extractor nodes use their ID directly: repo-scoped IDs embed the
// repo name, while shared entities use one ID across repos so the same topic
// or package merges into a single node. Hashing those with the repo would
// split each shared entity into per-repo copies and break cross-repo traversal.
//
// AST nodes hash repo + source_file + label + ID. The ID is required because
// (source_file, label) is not unique - one file can define many types with the
// same method name, and dropping the ID would merge them into one node.
func StableKey(repo string, n Node) string {
	if n.Origin == "platform" {
		return "platform::" + n.ID
	}
	h := sha1.New()
	h.Write([]byte(repo + "::" + n.SourceFile + "::" + n.Label + "::" + n.ID))
	return hex.EncodeToString(h.Sum(nil))
}

// extToLanguage maps source-file extensions to language names. Code only;
// config and doc formats are omitted so they don't show up as languages.
var extToLanguage = map[string]string{
	".go":     "go",
	".kt":     "kotlin",
	".kts":    "kotlin",
	".java":   "java",
	".swift":  "swift",
	".py":     "python",
	".ts":     "typescript",
	".tsx":    "typescript",
	".js":     "javascript",
	".jsx":    "javascript",
	".mjs":    "javascript",
	".cjs":    "javascript",
	".svelte": "svelte",
	".scala":  "scala",
	".rs":     "rust",
	".c":      "c",
	".h":      "c",
	".cpp":    "cpp",
	".cc":     "cpp",
	".hpp":    "cpp",
	".cs":     "csharp",
	".rb":     "ruby",
	".php":    "php",
	".sql":    "sql",
	".sh":     "bash",
	".bash":   "bash",
	".ps1":    "powershell",
	".dart":   "dart",
	".groovy": "groovy",
	".proto":  "protobuf",
	".tf":     "terraform",
}

// InferLanguage resolves a node's language, preferring metadata.language and
// falling back to the source-file extension. "" means unknown or non-code.
func InferLanguage(n Node) string {
	if l := n.MetaString("language"); l != "" {
		return l
	}
	sf := strings.ToLower(n.SourceFile)
	if i := strings.LastIndex(sf, "."); i >= 0 {
		return extToLanguage[sf[i:]]
	}
	return ""
}
