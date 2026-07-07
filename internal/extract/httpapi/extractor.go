// Package httpapi extracts HTTP routes exposed by a repository across the
// major backend frameworks: gin/echo/chi/mux/net-http (Go), Spring
// (Java/Kotlin), Express (JS/TS), Flask/FastAPI/Django (Python), and ASP.NET
// attributes (.NET).
//
// Two complementary strategies:
//
//  1. Literal matchers (matchers.go): regexes that fire when the route path
//     is a string literal at the registration call site.
//  2. Go constant resolution (this file): many services register routes
//     through identifiers (router.POST(constants.UpdateLimitRoute, h)) with
//     nested Group(constants.XxxRoute) prefixes, so a pre-pass collects
//     string constants across the repo and a post-pass resolves identifier
//     args and group-prefix chains. Group prefixes are
//     only chained within a single file - a group passed across a function
//     boundary loses its parent prefix (the route still surfaces, with a
//     partial path).
//
// The extractor is intentionally heuristic - full-grammar parsing of every
// supported framework would balloon the codebase. Confidence on every
// emitted edge is INFERRED, reflecting the heuristic nature.
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
	MaxFileBytes int64 // skip files larger than this (defensive); 0 = 2 MiB default
}

func New() *Extractor { return &Extractor{MaxFileBytes: 2 * 1024 * 1024} }

func (e *Extractor) Name() string { return "httpapi" }

// route is the unified shape every language-specific matcher returns.
type route struct {
	Method  string // GET | POST | PUT | PATCH | DELETE | HEAD | OPTIONS | ANY
	Path    string
	Handler string // empty if not statically resolvable
	Line    int
	// Source is where the route was discovered. Empty (sourceCode) means a
	// source-scanning matcher inferred it; sourceOpenAPI means it came from a
	// committed spec. It drives the emitted confidence tier and metadata.
	Source string
	// Tags carries an OpenAPI operation's tags (empty for code routes),
	// preserved on the node for business/domain grouping in the UI.
	Tags []string
}

const (
	sourceCode    = "code"
	sourceOpenAPI = "openapi"
)

// heldRoute is one discovered route plus the file it was found in, buffered so
// the code-derived and spec-derived sets can be reconciled before any node is
// emitted (see reconcileRoutes).
type heldRoute struct {
	file string
	r    route
}

// matcher fingerprints a single source file by extension and applies the
// matching framework's regex set. Each language family lives in its own file.
type matcher func(line string, lineNum int) []route

// matchers per file extension. Each entry is a NON-OVERLAPPING set: matchGin
// covers gin / echo / chi-upper / generic recv.METHOD patterns; matchChi
// supplements with chi's lowercase aliases; matchGorillaMux and matchNetHTTP
// catch their respective specific shapes. Duplication across matchers would
// produce duplicate route nodes (caught only by Fragment.AddNode's dedup).
var matchers = map[string][]matcher{
	".go":   {matchGin, matchChi, matchGorillaMux, matchNetHTTP},
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
	// `AdminRoute = "/admin"` inside const blocks, plus `var X = "..."` and
	// `const X = "..."` single declarations. Deliberately does NOT match `:=`
	// locals - route constants live at package level.
	goConstStrRe = regexp.MustCompile(`^\s*(?:var\s+|const\s+)?([A-Za-z_][A-Za-z0-9_]*)\s*(?:string\s*)?=\s*"([^"]+)"`)
	// `v := recv.Group(arg, ...)` - arg may be an identifier or a literal.
	goGroupDefRe = regexp.MustCompile(`\b([A-Za-z_]\w*)\s*:?=\s*([A-Za-z_]\w*)\.Group\s*\(\s*([A-Za-z_][\w.]*|"[^"]*")`)
	// `recv.POST(pkg.Identifier, handler)` - uppercase verbs only, identifier
	// arg only; literal args are matchGin's job and lowercase .Get() is config
	// getter noise (see matchChi's comment).
	goIdentRouteRe = regexp.MustCompile(`\b([A-Za-z_]\w*)\.(GET|POST|PUT|DELETE|PATCH|HEAD|OPTIONS|Any)\s*\(\s*([A-Za-z_][\w.]*)\s*(?:,\s*([A-Za-z0-9_.]+))?`)
)

// registerGoConst stores a package-level string constant that could hold a
// route path. Whether a constant actually IS a path is decided by its use,
// not its spelling: only constants that later appear in route position
// (recv.METHOD(<const>, ...) or recv.Group(<const>, ...)) are ever resolved,
// so we capture broadly here and let that usage be the filter. Values are
// rejected only when they clearly cannot be a path segment - they contain
// whitespace or look like a full URL.
//
// This deliberately does NOT require a leading "/" or a route/path/endpoint
// name hint. An earlier version did, and it silently dropped every
// identifier-arg route in repos whose convention is bare path segments with
// plain constant names (e.g. `Detail = "detail"`, `V2Group = "widgets/v2"`);
// normalizePath prepends the slash at emit time. First declaration wins on
// duplicate identifiers across packages.
func registerGoConst(m map[string]string, name, val string) {
	if val == "" || strings.ContainsAny(val, " \t") || strings.Contains(val, "://") {
		return
	}
	if _, exists := m[name]; !exists {
		m[name] = val
	}
}

func (e *Extractor) Extract(ctx context.Context, repoPath, repoName string) (*extract.Fragment, error) {
	frag := extract.NewFragment(e.Name())
	repoNodeID := "repo::" + repoName

	maxBytes := e.MaxFileBytes
	if maxBytes <= 0 {
		maxBytes = 2 * 1024 * 1024
	}

	// Go constant-resolution state, filled during the walk, resolved after.
	constVals := map[string]string{}
	groupDefs := map[string]map[string]goGroupDef{} // file -> var -> def
	var pending []goPendingRoute

	// Code-derived routes are buffered rather than emitted directly so they can
	// be reconciled against any OpenAPI spec before nodes are created. emit is
	// the sink every code path (matchers, JVM lookahead, Go const resolution)
	// funnels into.
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
		lineNum := 0
		for scanner.Scan() {
			lineNum++
			line := scanner.Text()
			if isGo {
				if m := goConstStrRe.FindStringSubmatch(line); m != nil {
					registerGoConst(constVals, m[1], m[2])
				}
				if m := goGroupDefRe.FindStringSubmatch(line); m != nil {
					if groupDefs[rel] == nil {
						groupDefs[rel] = map[string]goGroupDef{}
					}
					groupDefs[rel][m[1]] = goGroupDef{recv: m[2], arg: m[3]}
				}
				for _, m := range goIdentRouteRe.FindAllStringSubmatch(line, -1) {
					pending = append(pending, goPendingRoute{
						file: rel, line: lineNum,
						groupVar: m[1], method: m[2], arg: m[3], handler: m[4],
					})
				}
			}
			if isJVM {
				pendingJVM, classPrefix = resolveRequestMapping(emit, rel, line, pendingJVM, classPrefix)
				if m := requestMappingRe.FindStringSubmatch(line); m != nil {
					if classDeclRe.MatchString(line) {
						// Annotation and class declaration on one line
						// (common in Kotlin): it's a class-level prefix.
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
		// An annotation still pending at EOF annotated nothing we saw -
		// emit it as a route rather than dropping it silently.
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
	resolveArg := func(arg string) string {
		if strings.HasPrefix(arg, `"`) {
			return strings.Trim(arg, `"`)
		}
		if i := strings.LastIndex(arg, "."); i >= 0 {
			arg = arg[i+1:]
		}
		return constVals[arg]
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
		return joinPrefix(prefixOf(file, def.recv, depth+1), resolveArg(def.arg))
	}
	for _, pr := range pending {
		p := resolveArg(pr.arg)
		if p == "" {
			continue // identifier didn't resolve to a path-like constant
		}
		emit(pr.file, route{
			Method:  pr.method,
			Path:    joinPrefix(prefixOf(pr.file, pr.groupVar, 0), p),
			Handler: pr.handler,
			Line:    pr.line,
		})
	}

	// Reconcile code-derived routes with any committed OpenAPI/Swagger spec:
	// spec routes are authoritative on an exact (method, path) overlap, but
	// never suppress the undocumented/infra routes only the code path finds.
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

// pendingMapping is an @RequestMapping annotation whose role - class-level
// path prefix vs method-level route - is not yet known. Spring reuses the
// same annotation for both; only the declaration that follows disambiguates.
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
// line: a class/interface declaration promotes it to the class-level prefix,
// any other declaration means it was a method-level route (emitted here), and
// blank lines / comments / stacked annotations keep it pending. Returns the
// updated pending state and class prefix.
func resolveRequestMapping(emit func(string, route), file, line string, pending *pendingMapping, classPrefix string) (*pendingMapping, string) {
	if pending == nil {
		return nil, classPrefix
	}
	t := strings.TrimSpace(line)
	switch {
	case t == "" || strings.HasPrefix(t, "//") || strings.HasPrefix(t, "*") || strings.HasPrefix(t, "/*") || strings.HasPrefix(t, "@"):
		return pending, classPrefix // still looking past comments/annotations
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
	// A route from a committed spec is the authored contract (EXTRACTED); one
	// inferred by a source-scanning matcher stays INFERRED.
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

// reconcileRoutes merges the code-derived and spec-derived route sets. Spec
// routes are authoritative: on an exact (method, normalized-path) overlap the
// code route is dropped in favour of the spec's (correct path, EXTRACTED
// confidence, tags). Code routes that don't exactly match a spec route are all
// kept - that's the undocumented/infra surface a spec omits, and the signal
// that a route is exposed but not documented. Code-vs-code duplicates are left
// for emitRoute's node-id dedup (which is file-aware), so a repo with no spec
// is completely unaffected.
func reconcileRoutes(code, spec []heldRoute) []heldRoute {
	specKeys := make(map[string]bool, len(spec))
	emitted := make(map[string]bool, len(spec))
	out := make([]heldRoute, 0, len(spec)+len(code))
	for _, s := range spec {
		k := routeKey(s.r.Method, s.r.Path)
		specKeys[k] = true
		if emitted[k] {
			continue // collapse duplicate documentation of the same endpoint
		}
		emitted[k] = true
		out = append(out, s)
	}
	for _, c := range code {
		if specKeys[routeKey(c.r.Method, c.r.Path)] {
			continue // spec already covers this exact route, authoritatively
		}
		out = append(out, c)
	}
	return out
}

// routeKey is the reconciliation identity of a route: method + normalized path,
// independent of source file. Matches emitRoute's method/path normalization so
// a spec route and a code route for the same endpoint compare equal.
func routeKey(method, path string) string {
	return strings.ToUpper(method) + " " + normalizePath(path)
}

// infraPrefixes are path prefixes for operational endpoints (health, metrics,
// profiling, docs) as opposed to business API surface. Classification is
// advisory metadata for filtering/grouping in the UI; it does not affect which
// routes are emitted.
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
