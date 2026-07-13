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
		// Uppercase AND/OR/NOT are word operators to the query parser and get
		// phrase-quoted; lowercase (or embedded) forms are plain terms.
		{"boolean AND", "foo AND bar", `foo "AND" bar`},
		{"boolean OR", "foo OR bar", `foo "OR" bar`},
		{"boolean NOT", "NOT foo", `"NOT" foo`},
		{"lowercase and is a plain term", "foo and bar", `foo and bar`},
		{"AND embedded in a word", "OPERAND foo", `OPERAND foo`},
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
