package neo4j

import (
	"strings"
	"testing"
)

// capContext guards the "graph stores structure, never source text"
// invariant: context comes from graphify's output, so a release that starts
// emitting code snippets there must be truncated at import, not stored.
func TestCapContext(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"empty", "", ""},
		{"legitimate token", "parameter_type", "parameter_type"},
		{"deps scope", "runtime", "runtime"},
		{"exactly at cap", strings.Repeat("a", linkContextMax), strings.Repeat("a", linkContextMax)},
		{"one over cap", strings.Repeat("a", linkContextMax+1), strings.Repeat("a", linkContextMax)},
		{
			"source snippet is truncated",
			`if err := db.Exec("SELECT password FROM users WHERE id = ?", id); err != nil { return fmt.Errorf("query: %w", err) }`,
			`if err := db.Exec("SELECT password FROM users WHERE id = ?", id); err != `[:linkContextMax],
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := capContext(tc.in); got != tc.want {
				t.Errorf("capContext(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// Multi-byte runes must not be split mid-encoding by the cap.
func TestCapContextMultibyte(t *testing.T) {
	in := strings.Repeat("é", linkContextMax+10) // 2 bytes per rune: over cap in bytes and runes
	got := capContext(in)
	if want := strings.Repeat("é", linkContextMax); got != want {
		t.Errorf("multibyte truncation: got %d runes, want %d", len([]rune(got)), linkContextMax)
	}

	short := strings.Repeat("é", 40) // 80 bytes > cap, 40 runes < cap: must pass through
	if got := capContext(short); got != short {
		t.Errorf("40-rune multibyte string should be untouched, got %q", got)
	}
}
