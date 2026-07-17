// Package httpapi extracts HTTP routes across the major backend frameworks:
// gin/echo/chi/mux/net-http (Go), Spring (Java/Kotlin), Express (JS/TS),
// Flask/FastAPI/Django (Python), and ASP.NET (.NET).
//
// Three strategies: literal matchers (matchers.go) for string-literal paths
// at the call site; Go constant resolution (this file) for routes registered
// through identifiers, e.g. router.POST(constants.XxxRoute, h) with nested
// Group() prefixes, typed constants, raw strings, and concatenation chains;
// and OpenAPI/Swagger spec ingestion (openapi.go) as the authored contract.
// Formatter-wrapped registrations (arguments on their own lines) are joined
// and re-matched. Group prefixes are chained only within one file; a group
// passed across a function boundary loses its parent prefix (the route still
// surfaces, with a partial path). A route registration whose path cannot be
// resolved is dropped LOUDLY (fragment warning) - a silently missing route
// reads as false information to graph consumers.
//
// The extractor is heuristic, so every emitted edge is INFERRED.
package httpapi

import (
	"bufio"
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"graph-platform/internal/extract"
)

type Extractor struct {
	MaxFileBytes int64 // skip source files larger than this (defensive); 0 = 2 MiB default
	// SpecMaxBytes caps OpenAPI/Swagger spec files separately - generated
	// swagger.json files routinely exceed source-file size, and a spec is
	// the authored route contract, so it gets a much higher ceiling and a
	// loud warning (never a silent skip) when even that is exceeded.
	// 0 = 10 MiB default.
	SpecMaxBytes int64
}

func New() *Extractor {
	return &Extractor{
		MaxFileBytes: 2 * 1024 * 1024,
		SpecMaxBytes: 10 * 1024 * 1024,
	}
}

func (e *Extractor) Name() string { return "httpapi" }

// route is the shape every matcher returns.
type route struct {
	Method  string // GET | POST | PUT | PATCH | DELETE | HEAD | OPTIONS | ANY
	Path    string
	Handler string // empty if not statically resolvable
	Line    int
	Source  string   // sourceCode (inferred) or sourceOpenAPI (from a spec)
	Tags    []string // OpenAPI operation tags; empty for code routes
}

const (
	sourceCode    = "code"
	sourceOpenAPI = "openapi"
)

// heldRoute is one route plus its file, buffered so code and spec routes can
// be reconciled before any node is emitted.
type heldRoute struct {
	file string
	r    route
}

// matcher applies one framework's regex set to a source line.
type matcher func(line string, lineNum int) []route

// matchers per file extension. Each list is non-overlapping so the same route
// isn't emitted twice.
// Note: Go's uppercase-verb family (gin/echo `r.GET(...)`) is deliberately
// NOT in this table - it goes through the const/group-aware pending path
// (goIdentRouteRe) so group prefixes and constant paths resolve uniformly.
var matchers = map[string][]matcher{
	".go":   {matchChi, matchGorillaMux, matchNetHTTP},
	".py":   {matchFlaskFastAPI, matchDjango},
	".js":   {matchExpress},
	".jsx":  {matchExpress},
	".ts":   {matchExpress},
	".tsx":  {matchExpress},
	".mjs":  {matchExpress},
	".java": {matchSpring},
	".kt":   {matchSpring},
	".kts":  {matchSpring},
	".cs":   {matchAspNet},
	".fs":   {matchAspNet},
	".vb":   {matchAspNet},
	".rb":   {matchRails},
	".php":  {matchLaravel},
}

// --- Go constant-resolved route collection ---

// goGroupDef records one `v := recv.Group(arg, ...)` assignment so nested
// group prefixes can be chained (within one file) at resolution time.
type goGroupDef struct {
	recv string
	arg  string // identifier ("constants.AdminRoute") or quoted literal
}

// goPendingRoute is one identifier-arg route registration awaiting constant
// resolution after the walk completes.
type goPendingRoute struct {
	file     string
	line     int
	groupVar string
	method   string
	arg      string
	handler  string
}

var (
	// Package-level string-constant declarations, captured as a full
	// right-hand-side expression so all the shapes seen in real route
	// constants resolve: plain literals (`X = "/v1"`), explicitly or
	// custom-typed constants (`X string = "/v1"`, `X Route = "/v1"`), raw
	// strings (backticks), and concatenation chains (`X = V1 + "/deposit"`).
	// Does not match `:=` locals (the `:` can't be consumed by any group).
	goConstExprRe = regexp.MustCompile(`^\s*(?:var\s+|const\s+)?([A-Za-z_]\w*)(?:\s+[A-Za-z_][\w.\[\]]*)?\s*=\s*(.+)$`)
	// One token of a constant expression: identifier (possibly pkg-qualified).
	goIdentTokenRe = regexp.MustCompile(`^[A-Za-z_][\w.]*$`)
	// `x := <expr>` short variable declarations whose RHS might be a string
	// path. Registered file-scoped (see localExprs).
	goLocalAssignRe = regexp.MustCompile(`^\s*([A-Za-z_]\w*)\s*:=\s*(.+)$`)
	// `v := recv.Group(arg, ...)`; arg may be an identifier or a literal.
	goGroupDefRe = regexp.MustCompile(`\b([A-Za-z_]\w*)\s*:?=\s*([A-Za-z_]\w*)\.Group\s*\(\s*([A-Za-z_][\w.]*|"[^"]*")`)
	// `recv.POST(arg, handler)`: uppercase verbs (gin/echo style), arg either
	// an identifier (pkg.Constant) or a string literal. Both flow through the
	// same pending-resolution path so group prefixes chain for literals too -
	// `group.GET("/charges", h)` must get group's prefix exactly like
	// `group.GET(constants.Charges, h)`. Lowercase .Get() is config-getter
	// noise and stays with matchChi's path-shaped filter.
	goIdentRouteRe = regexp.MustCompile(`\b([A-Za-z_]\w*)\.(GET|POST|PUT|DELETE|PATCH|HEAD|OPTIONS|Any)\s*\(\s*([A-Za-z_][\w.]*|"[^"]*")\s*(?:,\s*([A-Za-z0-9_.]+))?`)
	// A route/group call whose argument list starts on the NEXT line
	// (formatter-wrapped registration). Detected so the following lines can
	// be joined and re-matched - otherwise the whole registration is
	// invisible to the per-line regexes and the route silently vanishes.
	goWrapOpenRe = regexp.MustCompile(`\.(?:GET|POST|PUT|DELETE|PATCH|HEAD|OPTIONS|Any|Get|Post|Put|Delete|Patch|Options|Head|Group|Handle|HandleFunc)\s*\(\s*$`)
)

// goWrapMaxLines / goWrapMaxBytes bound how far a wrapped registration is
// followed before giving up; a gofmt-wrapped call is a handful of lines.
const (
	goWrapMaxLines = 10
	goWrapMaxBytes = 4096
)

// registerGoConstExpr stores a package-level string-constant declaration for
// post-walk resolution. Whether it IS a route path is decided by use, not
// spelling: only constants that later appear in route position get resolved.
// The raw expression is validated (every `+`-separated part must be a string
// literal or an identifier) but not resolved here - referenced identifiers
// may live in files not walked yet. First declaration wins on duplicates.
func registerGoConstExpr(m map[string]string, name, rhs string) {
	rhs = strings.TrimSpace(stripGoLineComment(rhs))
	parts, ok := splitConcat(rhs)
	if !ok || len(parts) == 0 {
		return
	}
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if len(p) >= 2 && (p[0] == '"' && p[len(p)-1] == '"' || p[0] == '`' && p[len(p)-1] == '`') {
			continue
		}
		if goIdentTokenRe.MatchString(p) {
			continue
		}
		return // function call, number, composite - not a string constant
	}
	if _, exists := m[name]; !exists {
		m[name] = rhs
	}
}

// splitConcat splits a Go expression on top-level `+`, respecting string
// literals (escaped quotes included). ok is false when quoting is unbalanced.
func splitConcat(expr string) ([]string, bool) {
	var parts []string
	var cur strings.Builder
	inStr, inRaw, esc := false, false, false
	for i := 0; i < len(expr); i++ {
		c := expr[i]
		switch {
		case inRaw:
			cur.WriteByte(c)
			if c == '`' {
				inRaw = false
			}
		case inStr:
			cur.WriteByte(c)
			if esc {
				esc = false
			} else if c == '\\' {
				esc = true
			} else if c == '"' {
				inStr = false
			}
		case c == '"':
			inStr = true
			cur.WriteByte(c)
		case c == '`':
			inRaw = true
			cur.WriteByte(c)
		case c == '+':
			parts = append(parts, cur.String())
			cur.Reset()
		default:
			cur.WriteByte(c)
		}
	}
	if inStr || inRaw {
		return nil, false
	}
	parts = append(parts, cur.String())
	return parts, true
}

// stripGoComments removes comment content from one line - trailing //
// comments and /* */ block comments, tracking block state across lines so a
// commented-out registration block can never register routes. String-aware:
// "//" or "/*" inside a literal survives. Returns the code portion and
// whether a block comment is still open at end of line.
func stripGoComments(line string, inBlock bool) (string, bool) {
	var b strings.Builder
	inStr, inRaw, esc := false, false, false
	for i := 0; i < len(line); i++ {
		c := line[i]
		if inBlock {
			if c == '*' && i+1 < len(line) && line[i+1] == '/' {
				inBlock = false
				i++
			}
			continue
		}
		switch {
		case inRaw:
			b.WriteByte(c)
			if c == '`' {
				inRaw = false
			}
		case inStr:
			b.WriteByte(c)
			if esc {
				esc = false
			} else if c == '\\' {
				esc = true
			} else if c == '"' {
				inStr = false
			}
		case c == '"':
			inStr = true
			b.WriteByte(c)
		case c == '`':
			inRaw = true
			b.WriteByte(c)
		case c == '/' && i+1 < len(line) && line[i+1] == '/':
			return b.String(), false
		case c == '/' && i+1 < len(line) && line[i+1] == '*':
			inBlock = true
			i++
		default:
			b.WriteByte(c)
		}
	}
	return b.String(), inBlock
}

// stripGoLineComment removes a trailing // comment, string-aware so a "//"
// inside a literal survives.
func stripGoLineComment(s string) string {
	inStr, inRaw, esc := false, false, false
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case inRaw:
			if c == '`' {
				inRaw = false
			}
		case inStr:
			if esc {
				esc = false
			} else if c == '\\' {
				esc = true
			} else if c == '"' {
				inStr = false
			}
		case c == '"':
			inStr = true
		case c == '`':
			inRaw = true
		case c == '/' && i+1 < len(s) && s[i+1] == '/':
			return s[:i]
		}
	}
	return s
}

// parenDelta returns the net parenthesis depth change of a line, ignoring
// parens inside string literals and after a // comment. Drives the
// wrapped-registration joiner.
func parenDelta(line string) int {
	depth := 0
	inStr, inRaw, esc := false, false, false
	for i := 0; i < len(line); i++ {
		c := line[i]
		switch {
		case inRaw:
			if c == '`' {
				inRaw = false
			}
		case inStr:
			if esc {
				esc = false
			} else if c == '\\' {
				esc = true
			} else if c == '"' {
				inStr = false
			}
		default:
			switch c {
			case '"':
				inStr = true
			case '`':
				inRaw = true
			case '/':
				if i+1 < len(line) && line[i+1] == '/' {
					return depth
				}
			case '(':
				depth++
			case ')':
				depth--
			}
		}
	}
	return depth
}

func (e *Extractor) Extract(ctx context.Context, repoPath, repoName string) (*extract.Fragment, error) {
	frag := extract.NewFragment(e.Name())
	repoNodeID := "repo::" + repoName

	maxBytes := e.MaxFileBytes
	if maxBytes <= 0 {
		maxBytes = 2 * 1024 * 1024
	}

	// Go constant-resolution state, filled during the walk, resolved after.
	constExprs := map[string]string{}               // package-level: name -> raw RHS expression
	localExprs := map[string]map[string]string{}    // file-scoped `x := "..."` locals: file -> name -> RHS
	groupDefs := map[string]map[string]goGroupDef{} // file -> var -> def
	var pending []goPendingRoute

	// Buffer code-derived routes so they can be reconciled against an OpenAPI
	// spec before any node is created. Every code path funnels through emit.
	var codeRoutes []heldRoute
	emit := func(file string, r route) {
		codeRoutes = append(codeRoutes, heldRoute{file: file, r: r})
	}

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
		ms, ok := matchers[ext]
		if !ok {
			return nil
		}
		info, statErr := d.Info()
		if statErr != nil || info.Size() > maxBytes {
			return nil
		}
		rel, _ := filepath.Rel(repoPath, path)
		rel = filepath.ToSlash(rel)

		f, ferr := os.Open(path)
		if ferr != nil {
			frag.Warn(fmt.Sprintf("%s: %v", rel, ferr))
			return nil
		}
		scanner := bufio.NewScanner(f)
		scanner.Buffer(make([]byte, 64*1024), 1024*1024)

		isGo := ext == ".go"
		isJVM := ext == ".java" || ext == ".kt" || ext == ".kts"
		classPrefix := ""
		var pendingJVM *pendingMapping

		// processGoRouteText applies the Go route/group regexes plus the Go
		// literal matchers to one piece of text - a physical line during the
		// scan, or a joined wrapped registration at carry flush.
		processGoRouteText := func(text string, lineNum int) {
			if m := goGroupDefRe.FindStringSubmatch(text); m != nil {
				if groupDefs[rel] == nil {
					groupDefs[rel] = map[string]goGroupDef{}
				}
				groupDefs[rel][m[1]] = goGroupDef{recv: m[2], arg: m[3]}
			}
			for _, m := range goIdentRouteRe.FindAllStringSubmatch(text, -1) {
				pending = append(pending, goPendingRoute{
					file: rel, line: lineNum,
					groupVar: m[1], method: m[2], arg: m[3], handler: m[4],
				})
			}
		}

		// Wrapped-registration carry: a route/group call whose `(` ends the
		// line has its arguments on following lines; join them (bounded) and
		// re-match, or a formatter-wrapped route silently disappears.
		carry := ""
		carryStart, carryDepth := 0, 0
		inBlockComment := false

		lineNum := 0
		for scanner.Scan() {
			lineNum++
			line := scanner.Text()
			if isGo {
				// Comments are not code: a commented-out registration block
				// must neither emit routes (false positives in the graph)
				// nor warn about identifiers its dead declarations no longer
				// define. Everything below sees only the code portion.
				line, inBlockComment = stripGoComments(line, inBlockComment)
				if m := goConstExprRe.FindStringSubmatch(line); m != nil {
					registerGoConstExpr(constExprs, m[1], m[2])
				}
				// `x := "/health"` locals used in route position. File-scoped:
				// short local names (path, url) recur across files with
				// different values, so they must never collide globally.
				if m := goLocalAssignRe.FindStringSubmatch(line); m != nil {
					if localExprs[rel] == nil {
						localExprs[rel] = map[string]string{}
					}
					registerGoConstExpr(localExprs[rel], m[1], m[2])
				}
				if carry != "" {
					carry += " " + strings.TrimSpace(line)
					carryDepth += parenDelta(line)
					if carryDepth <= 0 || lineNum-carryStart >= goWrapMaxLines || len(carry) > goWrapMaxBytes {
						processGoRouteText(carry, carryStart)
						for _, m := range ms {
							for _, r := range m(carry, carryStart) {
								emit(rel, r)
							}
						}
						carry = ""
					}
				} else if goWrapOpenRe.MatchString(line) {
					carry = line
					carryStart, carryDepth = lineNum, parenDelta(line)
				}
				processGoRouteText(line, lineNum)
			}
			if isJVM {
				pendingJVM, classPrefix = resolveRequestMapping(emit, rel, line, pendingJVM, classPrefix)
				if m := requestMappingRe.FindStringSubmatch(line); m != nil {
					if classDeclRe.MatchString(line) {
						// Annotation and class decl on one line: class-level prefix.
						classPrefix = m[1]
					} else {
						pendingJVM = &pendingMapping{path: m[1], method: m[2], line: lineNum}
					}
					continue
				}
			}
			for _, m := range ms {
				for _, r := range m(line, lineNum) {
					if isJVM && classPrefix != "" {
						r.Path = joinPrefix(classPrefix, r.Path)
					}
					emit(rel, r)
				}
			}
		}
		if serr := scanner.Err(); serr != nil {
			frag.Warn(fmt.Sprintf("%s: scan: %v", rel, serr))
		}
		// A wrapped registration still open at EOF (truncated file): match
		// what was gathered rather than drop it.
		if isGo && carry != "" {
			processGoRouteText(carry, carryStart)
			for _, m := range ms {
				for _, r := range m(carry, carryStart) {
					emit(rel, r)
				}
			}
		}
		// An annotation still pending at EOF: emit it rather than drop it.
		if pendingJVM != nil {
			emitPendingAsRoute(emit, rel, pendingJVM, classPrefix)
		}
		_ = f.Close()
		return nil
	}

	if err := filepath.WalkDir(repoPath, walk); err != nil {
		return frag, fmt.Errorf("walk repo: %w", err)
	}

	// Resolve identifier-arg routes now that every file's constants are known.
	// Expressions resolve recursively (V1 + "/deposit" where V1 is itself a
	// constant), memoized, with a depth cap against cycles. File-scoped
	// locals win over package-level constants for the file they appear in.
	// Resolution distinguishes "resolved to a value" (which may legitimately
	// be "" - constants.EMPTY is the register-on-the-group's-own-path idiom)
	// from "did not resolve at all"; only the latter is a dropped route.
	memo := map[string]string{}
	memoOK := map[string]bool{}
	var resolveConstName func(file, name string, depth int) (string, bool)
	resolveConstName = func(file, name string, depth int) (string, bool) {
		if depth > 8 {
			return "", false
		}
		key := file + "\x00" + name
		if ok, seen := memoOK[key]; seen {
			return memo[key], ok
		}
		expr, found := localExprs[file][name]
		if !found {
			expr, found = constExprs[name]
		}
		if !found {
			memoOK[key] = false
			return "", false
		}
		parts, _ := splitConcat(expr)
		var b strings.Builder
		resolved := true
		for _, p := range parts {
			p = strings.TrimSpace(p)
			switch {
			case len(p) >= 2 && p[0] == '"' && p[len(p)-1] == '"':
				b.WriteString(p[1 : len(p)-1])
			case len(p) >= 2 && p[0] == '`' && p[len(p)-1] == '`':
				b.WriteString(p[1 : len(p)-1])
			case goIdentTokenRe.MatchString(p):
				nm := p
				if i := strings.LastIndex(nm, "."); i >= 0 {
					nm = nm[i+1:]
				}
				v, ok := resolveConstName(file, nm, depth+1)
				if !ok {
					resolved = false
				}
				b.WriteString(v)
			default:
				resolved = false
			}
			if !resolved {
				break
			}
		}
		val := b.String()
		// A value that can't be a route path (whitespace, full URL) counts
		// as unresolved; empty stays valid (group's-own-path idiom).
		if resolved && (strings.ContainsAny(val, " \t") || strings.Contains(val, "://")) {
			resolved = false
		}
		if !resolved {
			val = ""
		}
		memo[key], memoOK[key] = val, resolved
		return val, resolved
	}
	resolveArg := func(file, arg string) (string, bool) {
		if strings.HasPrefix(arg, `"`) {
			return strings.Trim(arg, `"`), true
		}
		nm := arg
		if i := strings.LastIndex(nm, "."); i >= 0 {
			nm = nm[i+1:]
		}
		return resolveConstName(file, nm, 0)
	}
	var prefixOf func(file, v string, depth int) string
	prefixOf = func(file, v string, depth int) string {
		if depth > 8 {
			return "" // defensive: cyclic or absurdly deep group chains
		}
		def, ok := groupDefs[file][v]
		if !ok {
			return ""
		}
		arg, _ := resolveArg(file, def.arg)
		return joinPrefix(prefixOf(file, def.recv, depth+1), arg)
	}
	for _, pr := range pending {
		p, ok := resolveArg(pr.file, pr.arg)
		if !ok {
			// A route registration whose path we couldn't resolve is a hole
			// in the graph - missing routes read as false information to
			// anyone querying it, so the drop must be loud, never silent.
			frag.Warn(fmt.Sprintf("%s:%d: %s route with path identifier %q dropped: does not resolve to a string constant",
				pr.file, pr.line, pr.method, pr.arg))
			continue
		}
		// An empty path (literal "" or a constant like EMPTY = "") is gin's
		// idiom for "register on the group's own path": the route IS the
		// prefix ("/" on a bare router).
		path := joinPrefix(prefixOf(pr.file, pr.groupVar, 0), p)
		if p == "" {
			path = prefixOf(pr.file, pr.groupVar, 0)
			if path == "" {
				path = "/"
			}
		}
		emit(pr.file, route{
			Method:  pr.method,
			Path:    path,
			Handler: pr.handler,
			Line:    pr.line,
		})
	}

	// Reconcile against any OpenAPI/Swagger spec: spec routes win on an exact
	// (method, path) overlap but never suppress code-only routes.
	specRoutes := e.openAPIRoutes(repoPath, frag)
	for _, hr := range reconcileRoutes(codeRoutes, specRoutes) {
		emitRoute(frag, repoNodeID, repoName, hr.file, hr.r)
	}

	// Emit the repo hub node ourselves so EXPOSES_ROUTE edges don't dangle
	// when the deps extractor (which also creates this hub) is disabled.
	if len(frag.Nodes) > 0 {
		frag.AddNode(extract.FragmentNode{
			ID:    repoNodeID,
			Label: repoName,
			Type:  "package",
			Metadata: map[string]any{
				"is_repository": true,
			},
		})
	}
	return frag, nil
}

// pendingMapping is an @RequestMapping whose role (class-level prefix vs
// method-level route) isn't known yet; the next declaration disambiguates it.
type pendingMapping struct {
	path   string
	method string // captured RequestMethod.X; empty means ANY
	line   int
}

var (
	requestMappingRe = regexp.MustCompile(`@RequestMapping\s*\(\s*(?:value\s*=\s*)?"([^"]+)"(?:[^)]*method\s*=\s*RequestMethod\.([A-Z]+))?`)
	classDeclRe      = regexp.MustCompile(`\b(?:class|interface|record|object)\s+\w+`)
)

// resolveRequestMapping advances a pending @RequestMapping against the next
// line: a class/interface decl makes it the class-level prefix, any other decl
// makes it a method-level route (emitted here), and blanks/comments/stacked
// annotations keep it pending.
func resolveRequestMapping(emit func(string, route), file, line string, pending *pendingMapping, classPrefix string) (*pendingMapping, string) {
	if pending == nil {
		return nil, classPrefix
	}
	t := strings.TrimSpace(line)
	switch {
	case t == "" || strings.HasPrefix(t, "//") || strings.HasPrefix(t, "*") || strings.HasPrefix(t, "/*") || strings.HasPrefix(t, "@"):
		return pending, classPrefix // still looking
	case classDeclRe.MatchString(t):
		return nil, pending.path
	default:
		emitPendingAsRoute(emit, file, pending, classPrefix)
		return nil, classPrefix
	}
}

func emitPendingAsRoute(emit func(string, route), file string, p *pendingMapping, classPrefix string) {
	method := p.method
	if method == "" {
		method = "ANY"
	}
	emit(file, route{
		Method: method,
		Path:   joinPrefix(classPrefix, p.path),
		Line:   p.line,
	})
}

// joinPrefix concatenates a class-level or group-level prefix and a
// method-level path per Spring/gin semantics: "/api" + "users" and
// "/api" + "/users" both resolve to "/api/users".
func joinPrefix(prefix, p string) string {
	if prefix == "" {
		return p
	}
	return strings.TrimRight(prefix, "/") + "/" + strings.TrimLeft(strings.TrimSpace(p), "/")
}

func emitRoute(frag *extract.Fragment, repoNodeID, repoName, file string, r route) {
	if r.Method == "" || r.Path == "" {
		return
	}
	method := strings.ToUpper(r.Method)
	path := normalizePath(r.Path)
	source := r.Source
	if source == "" {
		source = sourceCode
	}
	// Spec routes are the authored contract (EXTRACTED); code routes stay INFERRED.
	confidence := extract.ConfidenceInferred
	if source == sourceOpenAPI {
		confidence = extract.ConfidenceExtracted
	}
	id := "route::" + repoName + "::" + method + "::" + path + "::" + file
	meta := map[string]any{
		"method":         method,
		"path":           path,
		"handler":        r.Handler,
		"repo":           repoName,
		"source":         source,
		"classification": classifyRoute(path),
		"documented":     source == sourceOpenAPI,
	}
	if len(r.Tags) > 0 {
		meta["tags"] = r.Tags
	}
	frag.AddNode(extract.FragmentNode{
		ID:             id,
		Label:          method + " " + path,
		Type:           "http_route",
		SourceFile:     file,
		SourceLocation: fmt.Sprintf("L%d", r.Line),
		Metadata:       meta,
	})
	frag.AddEdge(extract.FragmentEdge{
		Source:         repoNodeID,
		Target:         id,
		Relation:       "exposes_route",
		Confidence:     confidence,
		SourceFile:     file,
		SourceLocation: fmt.Sprintf("L%d", r.Line),
	})
}

// reconcileRoutes merges the code and spec route sets. On an exact
// (method, path) overlap the spec route wins; code routes with no spec match
// are all kept (the undocumented/infra surface). Code-vs-code duplicates fall
// to emitRoute's file-aware node-id dedup, so a spec-less repo is unaffected.
func reconcileRoutes(code, spec []heldRoute) []heldRoute {
	specKeys := make(map[string]bool, len(spec))
	emitted := make(map[string]bool, len(spec))
	out := make([]heldRoute, 0, len(spec)+len(code))
	for _, s := range spec {
		k := routeKey(s.r.Method, s.r.Path)
		specKeys[k] = true
		if emitted[k] {
			continue // same endpoint documented twice
		}
		emitted[k] = true
		out = append(out, s)
	}
	for _, c := range code {
		if specKeys[routeKey(c.r.Method, c.r.Path)] {
			continue // spec already covers this exact route
		}
		out = append(out, c)
	}
	return out
}

// routeKey is a route's reconciliation identity: method + normalized path,
// matching emitRoute's normalization so a spec and code route compare equal.
func routeKey(method, path string) string {
	return strings.ToUpper(method) + " " + normalizePath(path)
}

// infraPrefixes flag operational endpoints (health, metrics, profiling, docs)
// vs business API surface. Advisory metadata only; doesn't affect emission.
var infraPrefixes = []string{
	"/health", "/healthz", "/livez", "/readyz", "/ready", "/live",
	"/metrics", "/debug/", "/debug", "/actuator", "/swagger", "/openapi",
	"/ping", "/version", "/status", "/__",
}

func classifyRoute(path string) string {
	p := strings.ToLower(path)
	for _, pre := range infraPrefixes {
		if p == pre || strings.HasPrefix(p, pre+"/") || (strings.HasSuffix(pre, "/") && strings.HasPrefix(p, pre)) {
			return "infra"
		}
	}
	return "business"
}

func normalizePath(p string) string {
	p = strings.TrimSpace(p)
	// Strip wrapping quotes if any leaked through.
	p = strings.Trim(p, `"' `)
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	return p
}

func shouldSkipDir(name string) bool {
	switch name {
	case ".git", "node_modules", "vendor", "target", "build", "dist",
		"__pycache__", ".venv", "venv", ".tox", ".gradle", ".idea",
		".vs", "bin", "obj", ".mvn", "tests", "test", "graphify-out":
		return true
	}
	return false
}
