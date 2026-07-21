package query

import "testing"

func TestNormalizeSymbol(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"plain name", "ConvertPosition", "convertposition"},
		{"trailing empty parens", "ConvertPosition()", "convertposition"},
		{"trailing parens with args", "ConvertPosition(a, b)", "convertposition"},
		{"surrounding whitespace", "  ConvertPosition()  ", "convertposition"},
		{"space before parens", "ConvertPosition ()", "convertposition"},
		{"empty", "", ""},
		{"whitespace only", "   ", ""},
		{"parens only (no name before them, left alone)", "()", "()"},
		{"leading paren (method-value syntax, left alone)", "(*Provider).ConvertPosition", "(*provider).convertposition"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := normalizeSymbol(tt.in); got != tt.want {
				t.Errorf("normalizeSymbol(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}
