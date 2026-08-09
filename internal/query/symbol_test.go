package query

import (
	"reflect"
	"testing"
)

// symbolMatchList must always offer every spelling: graphify stores Function
// names with a trailing "()" while other kinds are bare, and stores Swift and
// Kotlin members receiver-qualified (".formatLtp()"), so matching only one
// form silently loses the other kinds entirely.
func TestSymbolMatchList(t *testing.T) {
	tests := []struct {
		in   string
		want []string
	}{
		{"GetDepositService", []string{"getdepositservice", "getdepositservice()", ".getdepositservice", ".getdepositservice()"}},
		{"GetDepositService()", []string{"getdepositservice", "getdepositservice()", ".getdepositservice", ".getdepositservice()"}},
		{"GetDepositService(a, b)", []string{"getdepositservice", "getdepositservice()", ".getdepositservice", ".getdepositservice()"}},
		{"  GetDepositService ()  ", []string{"getdepositservice", "getdepositservice()", ".getdepositservice", ".getdepositservice()"}},
		// Internal dots are part of the name (SQL schema qualifier); only a
		// leading receiver dot is stripped.
		{"dbo.Orders", []string{"dbo.orders", "dbo.orders()", ".dbo.orders", ".dbo.orders()"}},
		// A user who types the stored receiver form gets the same set as one
		// who types the bare name - neither spelling loses the other.
		{".formatLtp()", []string{"formatltp", "formatltp()", ".formatltp", ".formatltp()"}},
		{"", []string{""}},
		{"()", []string{"()", ".()"}},
	}
	for _, tt := range tests {
		if got := symbolMatchList(tt.in); !reflect.DeepEqual(got, tt.want) {
			t.Errorf("symbolMatchList(%q) = %v, want %v", tt.in, got, tt.want)
		}
	}
}

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
