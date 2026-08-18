// Package postgres extracts PostgreSQL schema objects from .sql files:
// schemas, tables, views (including materialized), functions, procedures and
// triggers. Each object becomes a typed node; relationships (view reads
// table, function reads/writes table, trigger on table) are inferred from
// statement bodies.
//
// It emits the same sql_* node types and sql::<kind>::<schema>.<name> keys as
// the mssql extractor, so Postgres objects appear in the existing SQL query
// surfaces unchanged. The two never process the same file: sqldialect.Owner
// routes each .sql file to exactly one of them, because both would otherwise
// match a plain CREATE TABLE and disagree about the default schema (public
// here, dbo there) - which is part of the node key.
//
// Like mssql this is regex-based, not a parser: good for inventory and
// dependency graphs, not query analysis. Structural edges (object-to-schema,
// trigger-to-table) are EXTRACTED; body-derived dependency edges are
// INFERRED.
//
// Known gaps, all silent-by-design because warning on them would be noise:
// top-level INSERT seed data is attributed to the preceding object; dynamic
// SQL built in string literals is never scanned (string contents are masked,
// so a table named only inside EXECUTE '...' is invisible); and sequences,
// indexes, types and domains are boundaries only, not nodes - the query layer
// has no label for them.
package postgres

import (
	"bytes"
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"a1-knowledge-graph/internal/extract"
	"a1-knowledge-graph/internal/extract/sqldialect"
)

type Extractor struct {
	MaxFileBytes int64

	// DefaultDialect claims .sql files whose contents match neither dialect's
	// syntax - most plain DDL. Zero value (Unknown) leaves them with the
	// mssql extractor; a Postgres-only org sets it to Postgres so its
	// dialect-neutral files are keyed to public rather than dbo.
	DefaultDialect sqldialect.Dialect
}

func New() *Extractor { return &Extractor{MaxFileBytes: 8 * 1024 * 1024} }

func (e *Extractor) Name() string { return "postgres" }

// objectKind enumerates the object types we surface. The values match the
// mssql extractor's on purpose - one set of node types for all SQL, so
// find_sql_object and the repo overview need no dialect awareness.
type objectKind string

const (
	kindSchema    objectKind = "sql_schema"
	kindTable     objectKind = "sql_table"
	kindView      objectKind = "sql_view"
	kindProcedure objectKind = "sql_procedure"
	kindTrigger   objectKind = "sql_trigger"
	kindFunction  objectKind = "sql_function"

	// kindBoundary is internal: a statement that ends the previous object's
	// body without being an object itself.
	kindBoundary objectKind = ""
)

// defaultSchema is what an unqualified object name resolves to. Postgres
// resolves against search_path, whose first entry is public by default; we do
// not track SET search_path, so an object created under a non-default
// search_path is keyed to public. Rare in committed DDL, which almost always
// qualifies or sets the schema explicitly.
const defaultSchema = "public"

// qual matches an optionally schema-qualified identifier, bare or
// double-quoted. Group 1 is the schema (may be empty), group 2 the name.
const qual = `(?:"?([A-Za-z_][A-Za-z0-9_$]*)"?\s*\.\s*)?"?([A-Za-z_][A-Za-z0-9_$]*)"?`

var (
	createSchemaRe  = regexp.MustCompile(`(?i)CREATE\s+SCHEMA\s+(?:IF\s+NOT\s+EXISTS\s+)?"?([A-Za-z_][A-Za-z0-9_$]*)"?`)
	createTableRe   = regexp.MustCompile(`(?i)CREATE\s+(?:(?:GLOBAL|LOCAL)\s+)?(?:(?:TEMP|TEMPORARY|UNLOGGED)\s+)?TABLE\s+(?:IF\s+NOT\s+EXISTS\s+)?` + qual)
	createViewRe    = regexp.MustCompile(`(?i)CREATE\s+(?:OR\s+REPLACE\s+)?(?:(?:TEMP|TEMPORARY|RECURSIVE)\s+)?VIEW\s+(?:IF\s+NOT\s+EXISTS\s+)?` + qual)
	createMatViewRe = regexp.MustCompile(`(?i)CREATE\s+MATERIALIZED\s+VIEW\s+(?:IF\s+NOT\s+EXISTS\s+)?` + qual)
	createFuncRe    = regexp.MustCompile(`(?i)CREATE\s+(?:OR\s+REPLACE\s+)?FUNCTION\s+` + qual)
	createProcRe    = regexp.MustCompile(`(?i)CREATE\s+(?:OR\s+REPLACE\s+)?PROCEDURE\s+` + qual)

	// A Postgres trigger name is not schema-qualified - the trigger belongs
	// to its table's schema, which is why the ON target is captured here and
	// used as the trigger's own schema rather than defaulting.
	createTriggerRe = regexp.MustCompile(`(?i)CREATE\s+(?:OR\s+REPLACE\s+)?(?:CONSTRAINT\s+)?TRIGGER\s+"?([A-Za-z_][A-Za-z0-9_$]*)"?[\s\S]{0,400}?\bON\s+` + qual)

	// The function a trigger fires, from its EXECUTE FUNCTION/PROCEDURE tail.
	triggerExecRe = regexp.MustCompile(`(?i)\bEXECUTE\s+(?:FUNCTION|PROCEDURE)\s+` + qual)

	// ALTER TABLE ... FOREIGN KEY ... REFERENCES is how Postgres migrations
	// normally add constraints, so unlike the mssql extractor - which treats
	// this as a known non-goal - it is a first-class edge source here.
	alterTableRe = regexp.MustCompile(`(?i)ALTER\s+TABLE\s+(?:IF\s+EXISTS\s+)?(?:ONLY\s+)?` + qual)
	foreignKeyRe = regexp.MustCompile(`(?i)\bFOREIGN\s+KEY\b[\s\S]{0,200}?\bREFERENCES\s+` + qual)

	// Statements that end the preceding object's body without defining one
	// of our node types.
	boundaryRe = regexp.MustCompile(`(?i)\b(?:CREATE\s+(?:UNIQUE\s+)?INDEX|CREATE\s+SEQUENCE|CREATE\s+EXTENSION|CREATE\s+TYPE|CREATE\s+DOMAIN|CREATE\s+POLICY|COMMENT\s+ON|GRANT\s+|REVOKE\s+|DROP\s+)`)

	// Body scans. ONLY is Postgres's no-inheritance qualifier and appears
	// between the keyword and the table name.
	bodyFromRe    = regexp.MustCompile(`(?is)\bFROM\s+(?:ONLY\s+)?` + qual)
	bodyJoinRe    = regexp.MustCompile(`(?is)\bJOIN\s+(?:ONLY\s+)?` + qual)
	bodyInsertRe  = regexp.MustCompile(`(?is)\bINSERT\s+INTO\s+(?:ONLY\s+)?` + qual)
	bodyUpdateRe  = regexp.MustCompile(`(?is)\bUPDATE\s+(?:ONLY\s+)?` + qual)
	bodyDeleteRe  = regexp.MustCompile(`(?is)\bDELETE\s+FROM\s+(?:ONLY\s+)?` + qual)
	bodyCallRe    = regexp.MustCompile(`(?is)\bCALL\s+` + qual)
	bodyPerformRe = regexp.MustCompile(`(?is)\bPERFORM\s+` + qual)

	// Inline column-level FK. Postgres allows the column list to be omitted
	// (it defaults to the target's primary key), so unlike the T-SQL form a
	// trailing "(" cannot be required here.
	bodyReferencesRe = regexp.MustCompile(`(?is)\bREFERENCES\s+` + qual)

	// deleteFromRe marks the FROM of a DELETE FROM so the reads scan skips
	// it - a delete is a write, and must not also read.
	deleteFromRe = regexp.MustCompile(`(?is)\bDELETE\s+(FROM)\b`)

	// CTE names, from both the leading WITH and each following comma. A CTE
	// is referenced exactly like a table but is not one, so without this
	// every WITH query invents table nodes. (graphify hit the same bug in
	// its own SQL extractor and fixed it in 0.9.38.)
	cteNameRe = regexp.MustCompile(`(?is)(?:\bWITH\s+(?:RECURSIVE\s+)?|,\s*)"?([A-Za-z_][A-Za-z0-9_$]*)"?\s+AS\s*(?:(?:NOT\s+)?MATERIALIZED\s*)?\(`)
)

// bodyRefSkipNames are identifiers that must never become object nodes. Two
// sources, same failure shape: a regex captured something that reads like a
// table name but is not one.
//
//   - SQL keywords: a regex can backtrack past the optional schema group and
//     capture the following keyword (e.g. "DELETE FROM ONLY" -> "ONLY" when
//     ONLY is misplaced). Blocklisting is more robust than tightening each.
//   - new/old: plpgsql's trigger row records, not tables.
//   - lateral/unnest and friends: set-returning constructs in FROM position.
//     Note the "(" guard in addRef already drops function calls in FROM
//     position; these cover the bare forms.
var bodyRefSkipNames = map[string]bool{
	"as": true, "set": true, "where": true, "select": true, "from": true,
	"into": true, "values": true, "join": true, "on": true, "only": true,
	"using": true, "returning": true, "and": true, "or": true, "not": true,
	"exists": true, "lateral": true, "when": true, "then": true, "else": true,
	"end": true, "if": true, "loop": true, "begin": true, "declare": true,
	"return": true, "table": true, "distinct": true, "all": true,
	"new": true, "old": true,
}

// objectStmt is one statement. body is the text from the end of the header to
// the statement's terminating semicolon.
type objectStmt struct {
	kind          objectKind
	schema        string
	name          string
	body          string
	file          string
	line          int
	materialized  bool
	triggerTarget [2]string // (schema, table) a trigger fires on
	alterTarget   [2]string // (schema, table) an ALTER TABLE modifies
}

func (e *Extractor) Extract(ctx context.Context, repoPath, repoName string) (*extract.Fragment, error) {
	frag := extract.NewFragment(e.Name())
	em := &emitter{frag: frag, repo: repoName, seen: map[string]bool{}}

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
		if strings.ToLower(filepath.Ext(path)) != ".sql" {
			return nil
		}
		info, statErr := d.Info()
		if statErr != nil || info.Size() > e.MaxFileBytes {
			return nil
		}
		rel, _ := filepath.Rel(repoPath, path)
		rel = filepath.ToSlash(rel)

		raw, rerr := os.ReadFile(path)
		if rerr != nil {
			frag.Warn(fmt.Sprintf("%s: %v", rel, rerr))
			return nil
		}
		// Dialect routing: only files this extractor owns.
		if sqldialect.Owner(string(raw), e.DefaultDialect) != sqldialect.Postgres {
			return nil
		}
		for _, s := range splitObjects(string(raw), rel) {
			em.emit(s)
		}
		return nil
	}

	if err := filepath.WalkDir(repoPath, walk); err != nil {
		return frag, fmt.Errorf("walk repo: %w", err)
	}
	return frag, nil
}

// splitObjects locates every statement header and pairs it with its body.
//
// Headers are matched against a mask where comments, string literals and
// dollar-quoted bodies are blanked, so a CREATE inside a function body or a
// comment is never mistaken for a new statement. Bodies come from a second
// mask that keeps dollar-quoted content (that IS the function body) but still
// drops comments and string literals, so commented-out SQL cannot reach the
// graph. Both masks preserve byte offsets, so a position found in one indexes
// correctly into the other.
func splitObjects(text, file string) []objectStmt {
	headerText := mask(text, true)
	bodyText := mask(text, false)

	type hit struct {
		idx, end int
		stmt     objectStmt
	}
	var hits []hit
	add := func(idx, end int, s objectStmt) {
		s.file = file
		s.line = lineNum(text, idx)
		hits = append(hits, hit{idx: idx, end: end, stmt: s})
	}

	for _, m := range createSchemaRe.FindAllStringSubmatchIndex(headerText, -1) {
		add(m[0], m[1], objectStmt{kind: kindSchema, name: group(headerText, m, 1)})
	}

	collect := func(re *regexp.Regexp, kind objectKind, materialized bool) {
		for _, m := range re.FindAllStringSubmatchIndex(headerText, -1) {
			schema := group(headerText, m, 1)
			if schema == "" {
				schema = defaultSchema
			}
			add(m[0], m[1], objectStmt{
				kind:         kind,
				schema:       schema,
				name:         group(headerText, m, 2),
				materialized: materialized,
			})
		}
	}
	// Materialized views first: createViewRe cannot match "CREATE
	// MATERIALIZED VIEW" (MATERIALIZED is not one of its optional words), so
	// the two never double-count the same statement.
	collect(createMatViewRe, kindView, true)
	collect(createTableRe, kindTable, false)
	collect(createViewRe, kindView, false)
	collect(createFuncRe, kindFunction, false)
	collect(createProcRe, kindProcedure, false)

	for _, m := range createTriggerRe.FindAllStringSubmatchIndex(headerText, -1) {
		targetSchema := group(headerText, m, 2)
		if targetSchema == "" {
			targetSchema = defaultSchema
		}
		target := [2]string{targetSchema, group(headerText, m, 3)}
		add(m[0], m[1], objectStmt{
			kind: kindTrigger,
			// A trigger lives in its table's schema, not in public.
			schema:        targetSchema,
			name:          group(headerText, m, 1),
			triggerTarget: target,
		})
	}

	for _, m := range alterTableRe.FindAllStringSubmatchIndex(headerText, -1) {
		schema := group(headerText, m, 1)
		if schema == "" {
			schema = defaultSchema
		}
		add(m[0], m[1], objectStmt{
			kind:        kindBoundary,
			alterTarget: [2]string{schema, group(headerText, m, 2)},
		})
	}
	for _, m := range boundaryRe.FindAllStringIndex(headerText, -1) {
		add(m[0], m[1], objectStmt{kind: kindBoundary})
	}

	sort.Slice(hits, func(i, j int) bool { return hits[i].idx < hits[j].idx })

	out := make([]objectStmt, 0, len(hits))
	for i, h := range hits {
		limit := len(text)
		if i+1 < len(hits) {
			limit = hits[i+1].idx
		}
		// A statement never extends past its own terminating semicolon, even
		// if the next header is far away (trailing seed data, a stray GRANT
		// we do not model). Without this the last object in a file absorbs
		// everything after it.
		if end := endOfStatement(headerText, h.end, limit); end < limit {
			limit = end
		}
		s := h.stmt
		if h.end <= limit {
			s.body = bodyText[h.end:limit]
		}
		out = append(out, s)
	}
	return out
}

// endOfStatement returns the offset of the semicolon terminating the
// statement that starts at from, or limit if there is none in range. Nesting
// is tracked so a semicolon inside a parenthesised list does not end the
// statement. It reads the header mask, where dollar-quoted bodies are already
// blanked - so a plpgsql body's internal semicolons are invisible here and a
// function ends at the semicolon after its closing $$.
func endOfStatement(headerText string, from, limit int) int {
	depth := 0
	for i := from; i < limit && i < len(headerText); i++ {
		switch headerText[i] {
		case '(':
			depth++
		case ')':
			if depth > 0 {
				depth--
			}
		case ';':
			if depth == 0 {
				return i
			}
		}
	}
	return limit
}

func group(s string, m []int, i int) string {
	if 2*i+1 >= len(m) || m[2*i] < 0 {
		return ""
	}
	return s[m[2*i]:m[2*i+1]]
}

// mask blanks the regions of text that must not be pattern-matched, keeping
// the result the same length as the input so offsets stay interchangeable.
// Line comments, block comments (which nest in Postgres) and single-quoted
// literals are always blanked. Dollar-quoted bodies are blanked only when
// blankDollar is set - for header detection, not for body scanning.
//
// Double-quoted identifiers are deliberately left intact: they are object
// names, and the header regexes must still match them.
func mask(text string, blankDollar bool) string {
	src := []byte(text)
	out := make([]byte, len(src))
	copy(out, src)
	blank := func(i, j int) {
		for k := i; k < j && k < len(out); k++ {
			if out[k] != '\n' {
				out[k] = ' '
			}
		}
	}

	i := 0
	for i < len(src) {
		switch {
		case src[i] == '-' && i+1 < len(src) && src[i+1] == '-':
			j := i
			for j < len(src) && src[j] != '\n' {
				j++
			}
			blank(i, j)
			i = j

		case src[i] == '/' && i+1 < len(src) && src[i+1] == '*':
			depth, j := 1, i+2
			for j < len(src) && depth > 0 {
				if src[j] == '/' && j+1 < len(src) && src[j+1] == '*' {
					depth++
					j += 2
					continue
				}
				if src[j] == '*' && j+1 < len(src) && src[j+1] == '/' {
					depth--
					j += 2
					continue
				}
				j++
			}
			blank(i, j)
			i = j

		case src[i] == '\'':
			j := i + 1
			for j < len(src) {
				if src[j] == '\'' {
					// '' is an escaped quote, not the end.
					if j+1 < len(src) && src[j+1] == '\'' {
						j += 2
						continue
					}
					j++
					break
				}
				j++
			}
			blank(i, j)
			i = j

		case src[i] == '"':
			// Quoted identifier: real content, skipped over but not blanked
			// so that a quote inside it cannot start a bogus string.
			j := i + 1
			for j < len(src) && src[j] != '"' {
				j++
			}
			i = j + 1

		case src[i] == '$':
			tag, ok := dollarTag(src, i)
			if !ok {
				i++
				continue
			}
			bodyStart := i + len(tag)
			end, bodyEnd := len(src), len(src)
			if rel := bytes.Index(src[bodyStart:], []byte(tag)); rel >= 0 {
				bodyEnd = bodyStart + rel
				end = bodyEnd + len(tag)
			}
			// Unterminated delimiters fall through with end at EOF rather
			// than resuming mid-literal.
			if blankDollar {
				blank(i, end)
			} else {
				// The body is the function's real SQL and must survive, but
				// its comments must not: a commented-out statement inside a
				// plpgsql body is exactly as invisible to the database as
				// one outside it. String literals inside the body are left
				// alone - dollar quoting exists so bodies need not escape
				// quotes, so hunting for a closing quote here risks blanking
				// real SQL when an apostrophe stands alone.
				blankComments(out, src, bodyStart, bodyEnd)
			}
			i = end

		default:
			i++
		}
	}
	return string(out)
}

// blankComments blanks line and block comments in src[from:to], writing into
// out. Used for the inside of a dollar-quoted body, which mask keeps but must
// not keep commented-out statements from.
func blankComments(out, src []byte, from, to int) {
	if to > len(src) {
		to = len(src)
	}
	blank := func(i, j int) {
		for k := i; k < j && k < len(out); k++ {
			if out[k] != '\n' {
				out[k] = ' '
			}
		}
	}
	i := from
	for i < to {
		switch {
		case src[i] == '-' && i+1 < to && src[i+1] == '-':
			j := i
			for j < to && src[j] != '\n' {
				j++
			}
			blank(i, j)
			i = j
		case src[i] == '/' && i+1 < to && src[i+1] == '*':
			depth, j := 1, i+2
			for j < to && depth > 0 {
				if src[j] == '/' && j+1 < to && src[j+1] == '*' {
					depth++
					j += 2
					continue
				}
				if src[j] == '*' && j+1 < to && src[j+1] == '/' {
					depth--
					j += 2
					continue
				}
				j++
			}
			blank(i, j)
			i = j
		default:
			i++
		}
	}
}

// dollarTag reads a $$ or $tag$ delimiter starting at i.
func dollarTag(src []byte, i int) (string, bool) {
	if src[i] != '$' {
		return "", false
	}
	j := i + 1
	for j < len(src) && (src[j] == '_' || isAlnum(src[j])) {
		j++
	}
	if j < len(src) && src[j] == '$' {
		return string(src[i : j+1]), true
	}
	return "", false
}

func isAlnum(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9')
}

// emitter accumulates one repo's objects. seen spans every statement and file
// in the run, because the same relationship is routinely stated twice: an
// inline REFERENCES in the CREATE TABLE and an ALTER TABLE ADD CONSTRAINT in
// a later migration describe one foreign key, not two.
type emitter struct {
	frag *extract.Fragment
	repo string
	seen map[string]bool
}

// addEdge appends an edge unless an identical one was already emitted.
// Nodes need no equivalent - Fragment.AddNode is idempotent per ID.
func (em *emitter) addEdge(e extract.FragmentEdge) {
	key := e.Relation + "\x00" + e.Source + "\x00" + e.Target
	if em.seen[key] {
		return
	}
	em.seen[key] = true
	em.frag.AddEdge(e)
}

func (em *emitter) emit(s objectStmt) {
	frag, repoName := em.frag, em.repo
	if s.kind == kindBoundary {
		// ALTER TABLE carries constraints even though it defines nothing.
		if s.alterTarget[1] != "" {
			em.emitAlterRefs(s)
		}
		return
	}

	schema := s.schema
	if s.kind == kindSchema {
		schema = s.name
	}
	if schema == "" {
		schema = defaultSchema
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
	if s.name == "" {
		return
	}

	objectID := objectNodeID(s.kind, schema, s.name)
	meta := map[string]any{
		"schema":             schema,
		"object_name":        s.name,
		"discovered_in_repo": repoName,
	}
	if s.materialized {
		meta["materialized"] = true
	}
	frag.AddNode(extract.FragmentNode{
		ID:             objectID,
		Label:          schema + "." + s.name,
		Type:           string(s.kind),
		SourceFile:     s.file,
		SourceLocation: fmt.Sprintf("L%d", s.line),
		Metadata:       meta,
	})
	em.addEdge(extract.FragmentEdge{
		Source:         objectID,
		Target:         schemaNodeID,
		Relation:       "in_schema",
		Confidence:     extract.ConfidenceExtracted,
		SourceFile:     s.file,
		SourceLocation: fmt.Sprintf("L%d", s.line),
	})

	if s.kind == kindTrigger {
		targetID := objectNodeID(kindTable, s.triggerTarget[0], s.triggerTarget[1])
		declare(frag, targetID, kindTable, s.triggerTarget[0], s.triggerTarget[1])
		em.addEdge(extract.FragmentEdge{
			Source:         objectID,
			Target:         targetID,
			Relation:       "triggers_on",
			Confidence:     extract.ConfidenceExtracted,
			SourceFile:     s.file,
			SourceLocation: fmt.Sprintf("L%d", s.line),
		})
		// The function the trigger fires is in the header tail, which lands
		// in the body slice after the ON clause.
		if m := triggerExecRe.FindStringSubmatchIndex(s.body); m != nil {
			fnSchema := group(s.body, m, 1)
			if fnSchema == "" {
				fnSchema = defaultSchema
			}
			fnName := group(s.body, m, 2)
			if fnName != "" && !bodyRefSkipNames[strings.ToLower(fnName)] {
				fnID := objectNodeID(kindFunction, fnSchema, fnName)
				declare(frag, fnID, kindFunction, fnSchema, fnName)
				em.addEdge(extract.FragmentEdge{
					Source:     objectID,
					Target:     fnID,
					Relation:   "depends_on_object",
					Confidence: extract.ConfidenceExtracted,
					SourceFile: s.file,
				})
			}
		}
	}

	em.emitBodyRefs(objectID, s)
}

// emitAlterRefs handles ALTER TABLE ... ADD CONSTRAINT ... FOREIGN KEY ...
// REFERENCES, the normal way a Postgres migration adds a relationship. The
// edge is attributed to the altered table, forward-declaring both ends so it
// resolves whether or not either table is defined in this file.
func (em *emitter) emitAlterRefs(s objectStmt) {
	frag := em.frag
	matches := foreignKeyRe.FindAllStringSubmatchIndex(s.body, -1)
	if len(matches) == 0 {
		return
	}
	srcID := objectNodeID(kindTable, s.alterTarget[0], s.alterTarget[1])
	declare(frag, srcID, kindTable, s.alterTarget[0], s.alterTarget[1])
	for _, m := range matches {
		tSchema := group(s.body, m, 1)
		if tSchema == "" {
			tSchema = defaultSchema
		}
		tName := group(s.body, m, 2)
		if tName == "" || bodyRefSkipNames[strings.ToLower(tName)] {
			continue
		}
		tid := objectNodeID(kindTable, tSchema, tName)
		if tid == srcID {
			continue
		}
		declare(frag, tid, kindTable, tSchema, tName)
		em.addEdge(extract.FragmentEdge{
			Source:     srcID,
			Target:     tid,
			Relation:   "depends_on_object",
			Confidence: extract.ConfidenceInferred,
			SourceFile: s.file,
			Context:    "foreign key",
		})
	}
}

func (em *emitter) emitBodyRefs(sourceID string, s objectStmt) {
	frag := em.frag
	ctes := cteNames(s.body)

	deleteFroms := map[int]bool{}
	for _, m := range deleteFromRe.FindAllStringSubmatchIndex(s.body, -1) {
		deleteFroms[m[2]] = true // offset of the FROM keyword
	}

	addRef := func(re *regexp.Regexp, relation string, targetKind objectKind, skipCallForm bool) {
		for _, idx := range re.FindAllStringSubmatchIndex(s.body, -1) {
			if re == bodyFromRe && deleteFroms[idx[0]] {
				continue // this FROM belongs to a DELETE, already a write
			}
			tSchema, tName := defaultSchema, group(s.body, idx, 2)
			if g := group(s.body, idx, 1); g != "" {
				tSchema = g
			}
			if tName == "" || bodyRefSkipNames[strings.ToLower(tName)] {
				continue
			}
			// In FROM/JOIN position an identifier followed by "(" is a
			// set-returning function (unnest, generate_series, jsonb_to_record)
			// or a subquery alias, never a table.
			if skipCallForm && followedByParen(s.body, idx[1]) {
				continue
			}
			// A CTE is referenced exactly like a table but is not one.
			if group(s.body, idx, 1) == "" && ctes[strings.ToLower(tName)] {
				continue
			}
			tid := objectNodeID(targetKind, tSchema, tName)
			if tid == sourceID {
				continue // self-reference, e.g. a recursive view
			}
			declare(frag, tid, targetKind, tSchema, tName)
			em.addEdge(extract.FragmentEdge{
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
		addRef(bodyReferencesRe, "depends_on_object", kindTable, false)
	case kindView:
		addRef(bodyFromRe, "depends_on_object", kindTable, true)
		addRef(bodyJoinRe, "depends_on_object", kindTable, true)
	case kindFunction, kindProcedure:
		addRef(bodyFromRe, "reads_table", kindTable, true)
		addRef(bodyJoinRe, "reads_table", kindTable, true)
		addRef(bodyInsertRe, "writes_table", kindTable, false)
		addRef(bodyUpdateRe, "writes_table", kindTable, false)
		addRef(bodyDeleteRe, "writes_table", kindTable, false)
		addRef(bodyCallRe, "depends_on_object", kindProcedure, false)
		addRef(bodyPerformRe, "depends_on_object", kindFunction, false)
	case kindTrigger:
		addRef(bodyFromRe, "reads_table", kindTable, true)
		addRef(bodyJoinRe, "reads_table", kindTable, true)
		addRef(bodyInsertRe, "writes_table", kindTable, false)
		addRef(bodyUpdateRe, "writes_table", kindTable, false)
		addRef(bodyDeleteRe, "writes_table", kindTable, false)
	}
}

// cteNames collects the common-table-expression names bound in body, keyed
// lowercase.
func cteNames(body string) map[string]bool {
	out := map[string]bool{}
	for _, m := range cteNameRe.FindAllStringSubmatchIndex(body, -1) {
		if n := group(body, m, 1); n != "" {
			out[strings.ToLower(n)] = true
		}
	}
	return out
}

// followedByParen reports whether the next non-space byte after end is "(".
func followedByParen(s string, end int) bool {
	for i := end; i < len(s); i++ {
		switch s[i] {
		case ' ', '\t', '\r', '\n':
			continue
		case '(':
			return true
		default:
			return false
		}
	}
	return false
}

// declare forward-declares a referenced object so an edge resolves even when
// the object is defined in another file - or in another repo, which is the
// point: SQL objects are org-global shared nodes.
func declare(frag *extract.Fragment, id string, kind objectKind, schema, name string) {
	frag.AddNode(extract.FragmentNode{
		ID:    id,
		Label: schema + "." + name,
		Type:  string(kind),
		Metadata: map[string]any{
			"schema":      schema,
			"object_name": name,
		},
	})
}

func objectNodeID(kind objectKind, schema, name string) string {
	return fmt.Sprintf("sql::%s::%s.%s", kind, schema, name)
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
