// Package mssql extracts Microsoft SQL Server schema objects from .sql files:
// schemas, tables, views, procedures, triggers, and functions. Each object
// becomes a typed node; relationships (view reads table, procedure
// reads/writes table, trigger on table) are inferred from CREATE/ALTER bodies.
//
// It is regex-based, not a T-SQL parser: good for inventory and dependency
// graphs, not query analysis. Dependency edges are INFERRED; structural
// object-to-schema edges are EXTRACTED.
package mssql

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"a1-knowledge-graph/internal/extract"
)

type Extractor struct {
	MaxFileBytes int64
}

func New() *Extractor { return &Extractor{MaxFileBytes: 8 * 1024 * 1024} }

func (e *Extractor) Name() string { return "mssql" }

// objectKind enumerates the T-SQL object types we surface.
type objectKind string

const (
	kindSchema    objectKind = "sql_schema"
	kindTable     objectKind = "sql_table"
	kindView      objectKind = "sql_view"
	kindProcedure objectKind = "sql_procedure"
	kindTrigger   objectKind = "sql_trigger"
	kindFunction  objectKind = "sql_function"
)

var (
	createSchemaRe    = regexp.MustCompile(`(?i)CREATE\s+SCHEMA\s+\[?([A-Za-z0-9_]+)\]?`)
	createTableRe     = regexp.MustCompile(`(?i)CREATE\s+TABLE\s+(?:\[?([A-Za-z0-9_]+)\]?\.)?\[?([A-Za-z0-9_]+)\]?`)
	createViewRe      = regexp.MustCompile(`(?i)CREATE\s+(?:OR\s+ALTER\s+)?VIEW\s+(?:\[?([A-Za-z0-9_]+)\]?\.)?\[?([A-Za-z0-9_]+)\]?`)
	createProcedureRe = regexp.MustCompile(`(?i)CREATE\s+(?:OR\s+ALTER\s+)?PROC(?:EDURE)?\s+(?:\[?([A-Za-z0-9_]+)\]?\.)?\[?([A-Za-z0-9_]+)\]?`)
	createTriggerRe   = regexp.MustCompile(`(?i)CREATE\s+(?:OR\s+ALTER\s+)?TRIGGER\s+(?:\[?([A-Za-z0-9_]+)\]?\.)?\[?([A-Za-z0-9_]+)\]?[\s\S]+?ON\s+(?:\[?([A-Za-z0-9_]+)\]?\.)?\[?([A-Za-z0-9_]+)\]?`)
	createFunctionRe  = regexp.MustCompile(`(?i)CREATE\s+(?:OR\s+ALTER\s+)?FUNCTION\s+(?:\[?([A-Za-z0-9_]+)\]?\.)?\[?([A-Za-z0-9_]+)\]?`)

	// Body scans for cross-object references.
	bodySelectRe = regexp.MustCompile(`(?is)\bFROM\s+(?:\[?([A-Za-z0-9_]+)\]?\.)?\[?([A-Za-z0-9_]+)\]?`)
	bodyJoinRe   = regexp.MustCompile(`(?is)\bJOIN\s+(?:\[?([A-Za-z0-9_]+)\]?\.)?\[?([A-Za-z0-9_]+)\]?`)
	bodyInsertRe = regexp.MustCompile(`(?is)\bINSERT\s+INTO\s+(?:\[?([A-Za-z0-9_]+)\]?\.)?\[?([A-Za-z0-9_]+)\]?`)
	bodyUpdateRe = regexp.MustCompile(`(?is)\bUPDATE\s+(?:\[?([A-Za-z0-9_]+)\]?\.)?\[?([A-Za-z0-9_]+)\]?`)
	bodyDeleteRe = regexp.MustCompile(`(?is)\bDELETE\s+(?:FROM\s+)?(?:\[?([A-Za-z0-9_]+)\]?\.)?\[?([A-Za-z0-9_]+)\]?`)
	bodyExecRe   = regexp.MustCompile(`(?is)\bEXEC(?:UTE)?\s+(?:\[?([A-Za-z0-9_]+)\]?\.)?\[?([A-Za-z0-9_]+)\]?`)

	// bodyReferencesRe finds FOREIGN KEY ... REFERENCES target(...) inside a
	// CREATE TABLE body. The trailing "(" is required: it's what distinguishes
	// an actual FK target from an incidental mention, since real T-SQL FK
	// syntax always writes REFERENCES table(columns).
	//
	// Known non-goal: FKs added later via ALTER TABLE ADD CONSTRAINT aren't
	// caught - splitObjects only splits on CREATE statements, so a migration
	// file containing only an ALTER has no hit to anchor the edge to, and its
	// text is never scanned at all. Common migration-file pattern, not
	// handled this batch.
	bodyReferencesRe = regexp.MustCompile(`(?is)\bREFERENCES\s+(?:\[?([A-Za-z0-9_]+)\]?\.)?\[?([A-Za-z0-9_]+)\]?\s*\(`)
)

// bodyRefSkipNames are identifiers addRef must never turn into a node, keyed
// lowercase for a case-insensitive match. Two unrelated sources feed this one
// list because both are the same failure shape: a regex captured something
// that reads like a table/procedure name but isn't one.
//
//   - AS/SET/WHERE/SELECT/FROM/INTO/VALUES/JOIN/ON: a trigger header like
//     "AFTER UPDATE\nAS" backtracks past the (optional) schema-qualifier group
//     and captures the next keyword as if it were the table name (e.g.
//     "UPDATE AS" -> table "AS"). Blocklisting the keyword is simpler and more
//     robust than tightening every regex that could hit the same backtrack.
//   - inserted/deleted: T-SQL's trigger pseudo-tables, not real objects.
//     Filtered globally, not just inside trigger bodies - a real table named
//     inserted is vanishingly rare, and the noise cost of missing that edge
//     everywhere else is worse than the (very unlikely) miss.
//   - sp_executesql: the standard dynamic-SQL entry point; it shows up in EXEC
//     position constantly and is never a real procedure. Exact-match only -
//     the sp_ prefix is NOT blocklisted generally, because real shops
//     (especially older MSSQL codebases) name their own procedures
//     sp_Whatever, and prefix-filtering would silently drop those.
var bodyRefSkipNames = map[string]bool{
	"as": true, "set": true, "where": true, "select": true, "from": true,
	"into": true, "values": true, "join": true, "on": true,
	"inserted": true, "deleted": true,
	"sp_executesql": true,
}

// objectStmt is one CREATE statement. body is the text after the header, so
// the dependency regexes scan only this object's definition.
type objectStmt struct {
	kind          objectKind
	schema        string
	name          string
	body          string
	file          string
	line          int
	triggerTarget [2]string // (schema, table) for triggers
}

func (e *Extractor) Extract(ctx context.Context, repoPath, repoName string) (*extract.Fragment, error) {
	frag := extract.NewFragment(e.Name())

	walk := func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			if d != nil && d.IsDir() && shouldSkipDir(d.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		ext := strings.ToLower(filepath.Ext(path))
		if ext != ".sql" {
			return nil
		}
		info, statErr := d.Info()
		if statErr != nil || info.Size() > e.MaxFileBytes {
			return nil
		}
		rel, _ := filepath.Rel(repoPath, path)
		rel = filepath.ToSlash(rel)

		body, rerr := os.ReadFile(path)
		if rerr != nil {
			frag.Warn(fmt.Sprintf("%s: %v", rel, rerr))
			return nil
		}
		stmts := splitObjects(string(body), rel)
		for _, s := range stmts {
			emit(frag, repoName, s)
		}
		return nil
	}

	if err := filepath.WalkDir(repoPath, walk); err != nil {
		return frag, fmt.Errorf("walk repo: %w", err)
	}
	return frag, nil
}

// splitObjects returns each CREATE/ALTER statement with its body up to the next
// CREATE/ALTER or EOF. Without a real parser, the next CREATE keyword and GO
// separators act as statement delimiters.
func splitObjects(text, file string) []objectStmt {
	var out []objectStmt
	// Find all top-level CREATE positions in order.
	type hit struct {
		idx  int
		end  int
		stmt objectStmt
	}
	var hits []hit

	addHit := func(idx, end int, s objectStmt) { hits = append(hits, hit{idx: idx, end: end, stmt: s}) }

	for _, m := range createSchemaRe.FindAllStringSubmatchIndex(text, -1) {
		groups := createSchemaRe.FindStringSubmatch(text[m[0]:m[1]])
		addHit(m[0], m[1], objectStmt{kind: kindSchema, name: groups[1], file: file, line: lineNum(text, m[0])})
	}
	collect := func(re *regexp.Regexp, kind objectKind) {
		for _, m := range re.FindAllStringSubmatchIndex(text, -1) {
			groups := re.FindStringSubmatch(text[m[0]:m[1]])
			schema, name := "dbo", ""
			if len(groups) >= 3 {
				if groups[1] != "" {
					schema = groups[1]
				}
				name = groups[2]
			}
			addHit(m[0], m[1], objectStmt{
				kind:   kind,
				schema: schema,
				name:   name,
				file:   file,
				line:   lineNum(text, m[0]),
			})
		}
	}
	collect(createTableRe, kindTable)
	collect(createViewRe, kindView)
	collect(createProcedureRe, kindProcedure)
	collect(createFunctionRe, kindFunction)

	// Triggers carry an extra ON <target>.
	for _, m := range createTriggerRe.FindAllStringSubmatchIndex(text, -1) {
		groups := createTriggerRe.FindStringSubmatch(text[m[0]:m[1]])
		schema, name := "dbo", ""
		if groups[1] != "" {
			schema = groups[1]
		}
		name = groups[2]
		targetSchema, targetTable := "dbo", ""
		if groups[3] != "" {
			targetSchema = groups[3]
		}
		targetTable = groups[4]
		addHit(m[0], m[1], objectStmt{
			kind:          kindTrigger,
			schema:        schema,
			name:          name,
			file:          file,
			line:          lineNum(text, m[0]),
			triggerTarget: [2]string{targetSchema, targetTable},
		})
	}

	// Sort hits by start index ascending - establishes statement boundaries.
	sort.Slice(hits, func(i, j int) bool { return hits[i].idx < hits[j].idx })
	for i, h := range hits {
		bodyStart := h.end
		bodyEnd := len(text)
		if i+1 < len(hits) {
			bodyEnd = hits[i+1].idx
		}
		s := h.stmt
		s.body = text[bodyStart:bodyEnd]
		out = append(out, s)
	}
	return out
}

func emit(frag *extract.Fragment, repoName string, s objectStmt) {
	// CREATE SCHEMA carries the name in s.name; other statements use s.schema,
	// defaulting to "dbo" when unqualified.
	schema := s.schema
	if s.kind == kindSchema {
		schema = s.name
	}
	if schema == "" {
		schema = "dbo"
	}
	schemaNodeID := "sql::schema::" + schema
	frag.AddNode(extract.FragmentNode{
		ID:    schemaNodeID,
		Label: schema,
		Type:  string(kindSchema),
		Metadata: map[string]any{
			"discovered_in_repo": repoName,
		},
	})

	if s.kind == kindSchema {
		return
	}

	objectID := fmt.Sprintf("sql::%s::%s.%s", s.kind, schema, s.name)
	frag.AddNode(extract.FragmentNode{
		ID:             objectID,
		Label:          schema + "." + s.name,
		Type:           string(s.kind),
		SourceFile:     s.file,
		SourceLocation: fmt.Sprintf("L%d", s.line),
		Metadata: map[string]any{
			"schema":             schema,
			"object_name":        s.name,
			"discovered_in_repo": repoName,
		},
	})
	frag.AddEdge(extract.FragmentEdge{
		Source:         objectID,
		Target:         schemaNodeID,
		Relation:       "in_schema",
		Confidence:     extract.ConfidenceExtracted,
		SourceFile:     s.file,
		SourceLocation: fmt.Sprintf("L%d", s.line),
	})

	if s.kind == kindTrigger {
		targetID := fmt.Sprintf("sql::%s::%s.%s", kindTable, s.triggerTarget[0], s.triggerTarget[1])
		// Forward-declare the target table so the edge resolves even if the
		// table is defined in another file.
		frag.AddNode(extract.FragmentNode{
			ID:    targetID,
			Label: s.triggerTarget[0] + "." + s.triggerTarget[1],
			Type:  string(kindTable),
			Metadata: map[string]any{
				"schema":      s.triggerTarget[0],
				"object_name": s.triggerTarget[1],
			},
		})
		frag.AddEdge(extract.FragmentEdge{
			Source:         objectID,
			Target:         targetID,
			Relation:       "triggers_on",
			Confidence:     extract.ConfidenceExtracted,
			SourceFile:     s.file,
			SourceLocation: fmt.Sprintf("L%d", s.line),
		})
	}

	// Inferred read/write edges from the body.
	emitBodyRefs(frag, objectID, s)
}

// deleteFromRe marks the FROM in DELETE FROM so the reads scan skips it: a
// DELETE FROM must not also produce a reads_table edge.
var deleteFromRe = regexp.MustCompile(`(?is)\bDELETE\s+(FROM)\b`)

func emitBodyRefs(frag *extract.Fragment, sourceID string, s objectStmt) {
	deleteFroms := map[int]bool{}
	for _, m := range deleteFromRe.FindAllStringSubmatchIndex(s.body, -1) {
		deleteFroms[m[2]] = true // offset of the FROM keyword (group 1 start)
	}

	// targetKind defaults every caller to kindTable except the EXEC scan,
	// which targets a procedure - see the bodyExecRe call site below.
	addRef := func(re *regexp.Regexp, relation string, targetKind objectKind) {
		seen := map[string]bool{}
		for _, idx := range re.FindAllStringSubmatchIndex(s.body, -1) {
			if re == bodySelectRe && deleteFroms[idx[0]] {
				continue // this FROM belongs to a DELETE FROM, not a read
			}
			group := func(i int) string {
				if idx[2*i] < 0 {
					return ""
				}
				return s.body[idx[2*i]:idx[2*i+1]]
			}
			tSchema, tName := "dbo", group(2)
			if group(1) != "" {
				tSchema = group(1)
			}
			if tName == "" || bodyRefSkipNames[strings.ToLower(tName)] {
				continue
			}
			tid := fmt.Sprintf("sql::%s::%s.%s", targetKind, tSchema, tName)
			if seen[relation+":"+tid] {
				continue
			}
			seen[relation+":"+tid] = true
			frag.AddNode(extract.FragmentNode{
				ID:    tid,
				Label: tSchema + "." + tName,
				Type:  string(targetKind),
				Metadata: map[string]any{
					"schema":      tSchema,
					"object_name": tName,
				},
			})
			frag.AddEdge(extract.FragmentEdge{
				Source:     sourceID,
				Target:     tid,
				Relation:   relation,
				Confidence: extract.ConfidenceInferred,
				SourceFile: s.file,
			})
		}
	}
	switch s.kind {
	case kindTable:
		addRef(bodyReferencesRe, "depends_on_object", kindTable)
	case kindView:
		addRef(bodySelectRe, "depends_on_object", kindTable)
		addRef(bodyJoinRe, "depends_on_object", kindTable)
	case kindProcedure, kindFunction:
		addRef(bodySelectRe, "reads_table", kindTable)
		addRef(bodyJoinRe, "reads_table", kindTable)
		addRef(bodyInsertRe, "writes_table", kindTable)
		addRef(bodyUpdateRe, "writes_table", kindTable)
		addRef(bodyDeleteRe, "writes_table", kindTable)
		addRef(bodyExecRe, "depends_on_object", kindProcedure)
	case kindTrigger:
		addRef(bodyInsertRe, "writes_table", kindTable)
		addRef(bodyUpdateRe, "writes_table", kindTable)
		addRef(bodyDeleteRe, "writes_table", kindTable)
		addRef(bodySelectRe, "reads_table", kindTable)
	}
}

func lineNum(text string, offset int) int {
	if offset > len(text) {
		offset = len(text)
	}
	return 1 + strings.Count(text[:offset], "\n")
}

func shouldSkipDir(name string) bool {
	switch name {
	case ".git", "node_modules", "vendor", "target", "build", "dist",
		"__pycache__", ".venv", "venv", ".tox", ".gradle", ".idea",
		".vs", "bin", "obj", ".mvn":
		return true
	}
	return false
}
