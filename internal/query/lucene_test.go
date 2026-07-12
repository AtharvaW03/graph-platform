package query

import "testing"

func TestLuceneEscape(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"plain word", "ProcessPayment", "ProcessPayment"},
		{"empty", "", ""},
		{"whitespace only", "   ", ""},
		{"trims surrounding whitespace", "  ProcessPayment  ", "ProcessPayment"},
		{"wildcard", "foo*bar", `foo\*bar`},
		{"question mark wildcard", "foo?bar", `foo\?bar`},
		{"boolean AND", "foo AND bar", `foo AND bar`}, // bare word "AND" is not special, only the operators below are
		{"boolean operators", "foo&&bar||baz", `foo\&\&bar\|\|baz`},
		{"negation and required", "-foo +bar", `\-foo \+bar`},
		{"parens and brackets", "foo(bar)[baz]", `foo\(bar\)\[baz\]`},
		{"braces", "foo{bar}", `foo\{bar\}`},
		{"caret boost", "foo^2", `foo\^2`},
		{"tilde fuzzy", "foo~", `foo\~`},
		{"quote", `foo"bar`, `foo\"bar`},
		{"colon field marker", "foo:bar", `foo\:bar`},
		{"backslash", `foo\bar`, `foo\\bar`},
		{"slash", "foo/bar", `foo\/bar`},
		{"control characters stripped", "foo\x00\x01bar", "foobar"},
		{"kitchen sink", `foo*bar(baz) "AND" -x`, `foo\*bar\(baz\) \"AND\" \-x`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := luceneEscape(tt.in); got != tt.want {
				t.Errorf("luceneEscape(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}
