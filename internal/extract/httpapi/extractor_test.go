package httpapi

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"graph-platform/internal/extract"
)

// runExtractFrag writes files into a temp repo and returns the raw fragment,
// for tests that assert on warnings or metadata, not just route labels.
func runExtractFrag(t *testing.T, files map[string]string) *extract.Fragment {
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

// runExtract writes files into a temp repo and returns the route labels
// ("METHOD /path") the extractor emitted.
func runExtract(t *testing.T, files map[string]string) map[string]bool {
	t.Helper()
	frag := runExtractFrag(t, files)
	routes := map[string]bool{}
	for _, n := range frag.Nodes {
		if n.Type == "http_route" {
			routes[n.Label] = true
		}
	}
	return routes
}

func TestGoRouters(t *testing.T) {
	routes := runExtract(t, map[string]string{
		"main.go": `package main

func routes() {
	r.Get("/users", listUsers)
	router.GET("/gin/items", itemsHandler)
	m.HandleFunc("/legacy", legacyHandler).Methods("POST")
	http.HandleFunc("/health", healthHandler)
}
`,
	})
	for _, want := range []string{"GET /users", "GET /gin/items", "POST /legacy", "ANY /health"} {
		if !routes[want] {
			t.Errorf("missing route %q; got %v", want, routes)
		}
	}
}

// TestGoGetFalsePositives is the regression test for the config-key bug:
// x.Get("string") is everywhere in Go and must not become an HTTP route
// unless the argument looks like a path.
func TestGoGetFalsePositives(t *testing.T) {
	routes := runExtract(t, map[string]string{
		"config.go": `package main

func load() {
	port := viper.Get("server.port")
	v := cache.Get("user:42")
	h := headers.Get("Accept")
}
`,
	})
	if len(routes) != 0 {
		t.Errorf("config/cache lookups extracted as routes: %v", routes)
	}
}

func TestSpringClassPrefix(t *testing.T) {
	routes := runExtract(t, map[string]string{
		"UserController.java": `package com.example;

@RestController
@RequestMapping("/api/v1")
public class UserController {

    @GetMapping("/users")
    public List<User> list() { return svc.all(); }

    @RequestMapping(value = "/legacy", method = RequestMethod.POST)
    public void legacy() {}

    @PostMapping("orders")
    public void order() {}
}
`,
	})
	for _, want := range []string{"GET /api/v1/users", "POST /api/v1/legacy", "POST /api/v1/orders"} {
		if !routes[want] {
			t.Errorf("missing route %q; got %v", want, routes)
		}
	}
	// The class-level @RequestMapping must NOT itself become a route.
	if routes["ANY /api/v1"] {
		t.Errorf("class-level @RequestMapping emitted as a route: %v", routes)
	}
	if len(routes) != 3 {
		t.Errorf("route count = %d, want 3: %v", len(routes), routes)
	}
}

func TestSpringMethodLevelRequestMappingWithoutClass(t *testing.T) {
	routes := runExtract(t, map[string]string{
		"Handler.kt": `@RequestMapping("/ping")
fun ping(): String = "pong"
`,
	})
	if !routes["ANY /ping"] {
		t.Errorf("method-level @RequestMapping not emitted: %v", routes)
	}
}

func TestExpressAndFlask(t *testing.T) {
	routes := runExtract(t, map[string]string{
		"server.js": `const app = express();
app.get('/api/items', (req, res) => res.json([]));
app.post('/api/items', createItem);
`,
		"app.py": `@app.route('/flask', methods=['POST'])
def flask_handler():
    pass

@router.get("/fast")
def fast():
    pass
`,
	})
	for _, want := range []string{"GET /api/items", "POST /api/items", "POST /flask", "GET /fast"} {
		if !routes[want] {
			t.Errorf("missing route %q; got %v", want, routes)
		}
	}
}

// TestCrossLanguageCommentedRoutesIgnored: commented-out registrations in
// every non-Go language must not become routes, while live code (including
// lines with trailing comments and backtick template paths) still matches.
func TestCrossLanguageCommentedRoutesIgnored(t *testing.T) {
	routes := runExtract(t, map[string]string{
		"app.py": `# @app.route('/py-dead', methods=['POST'])
@app.route('/py-live', methods=['GET'])  # trailing comment
def handler():
    pass
`,
		"server.js": "// app.get('/js-dead-line', h);\n" +
			"/*\napp.post('/js-dead-block', h);\n*/\n" +
			"const tpl = `\n/* app.get('/js-dead-in-template', h) */\n`;\n" +
			"app.get(`/js-live-tpl`, h);\n" +
			"app.post('/js-live', h); // trailing\n",
		"Controller.java": `package com.example;

/**
 * Example javadoc, retired route kept for reference:
 * @GetMapping("/java-dead-javadoc")
 */
@RestController
public class Controller {
    // @GetMapping("/java-dead-line")
    @GetMapping("/java-live")
    public String live() { return "ok"; }
}
`,
		"routes.rb": `# get '/rb-dead'
get '/rb-live'
`,
	})

	for _, want := range []string{
		"GET /py-live", "GET /js-live-tpl", "POST /js-live", "GET /java-live", "GET /rb-live",
	} {
		if !routes[want] {
			t.Errorf("live route %q missing; got %v", want, routes)
		}
	}
	for label := range routes {
		if strings.Contains(label, "dead") {
			t.Errorf("commented-out code produced route %q", label)
		}
	}
}

func TestRepoHubEmitted(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "r.go"), []byte(`package main
func f() { r.Get("/x", h) }
`), 0o644); err != nil {
		t.Fatal(err)
	}
	frag, err := New().Extract(context.Background(), dir, "svc")
	if err != nil {
		t.Fatal(err)
	}
	var hub *extract.FragmentNode
	for i := range frag.Nodes {
		if frag.Nodes[i].ID == "repo::svc" {
			hub = &frag.Nodes[i]
		}
	}
	if hub == nil {
		t.Fatal("repo hub node missing - EXPOSES_ROUTE edges dangle when deps extractor is disabled")
	}
}
