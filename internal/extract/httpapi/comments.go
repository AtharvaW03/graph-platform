package httpapi

import "strings"

// Cross-language comment stripping. Dead code is not API surface: a
// commented-out `app.get('/old', h)` or `@GetMapping("/retired")` must not
// become a route in the graph. Go has its own stripper (extractor.go,
// integrated with the const/group state machine); every other supported
// language goes through the generic one here, configured per extension.
//
// Limits, biased to fail safe (a miss lets dead code through, never hides
// live code):
//   - quote state resets per line except JS template literals, the one
//     multiline string form likely to contain comment-lookalike text;
//   - Python/Kotlin triple-quoted strings are not tracked (a route example
//     inside a docstring may still match).
type commentSyntax struct {
	lineMarkers []string // "//", "#", "'" (VB)
	blockOpen   string   // "" = no block comments
	blockClose  string
	quotes      string // string-delimiter chars; comments inside them don't count
	// multilineQuote is a quote char whose strings span lines (JS backtick).
	// Its state persists across lines so a template literal containing "/*"
	// can't poison the block-comment state for the rest of the file.
	multilineQuote byte
}

var (
	cSyntax      = commentSyntax{lineMarkers: []string{"//"}, blockOpen: "/*", blockClose: "*/", quotes: `"'`}
	jsSyntax     = commentSyntax{lineMarkers: []string{"//"}, blockOpen: "/*", blockClose: "*/", quotes: "\"'`", multilineQuote: '`'}
	hashSyntax   = commentSyntax{lineMarkers: []string{"#"}, quotes: `"'`}
	phpSyntax    = commentSyntax{lineMarkers: []string{"//", "#"}, blockOpen: "/*", blockClose: "*/", quotes: `"'`}
	vbSyntax     = commentSyntax{lineMarkers: []string{"'"}, quotes: `"`}
	fsharpSyntax = commentSyntax{lineMarkers: []string{"//"}, blockOpen: "(*", blockClose: "*)", quotes: `"`}
)

// commentSyntaxByExt covers every extension in the matchers table except .go
// (which has its own stripper).
var commentSyntaxByExt = map[string]commentSyntax{
	".py":   hashSyntax,
	".rb":   hashSyntax,
	".js":   jsSyntax,
	".jsx":  jsSyntax,
	".ts":   jsSyntax,
	".tsx":  jsSyntax,
	".mjs":  jsSyntax,
	".java": cSyntax,
	".kt":   cSyntax,
	".kts":  cSyntax,
	".cs":   cSyntax,
	".fs":   fsharpSyntax,
	".vb":   vbSyntax,
	".php":  phpSyntax,
}

// stripState carries per-file comment state across lines.
type stripState struct {
	inBlock    bool // inside blockOpen..blockClose
	inMultiStr bool // inside a multiline string (JS template literal)
}

// stripComments returns the code portion of one line under the given syntax,
// updating st for constructs that span lines.
func stripComments(line string, s commentSyntax, st *stripState) string {
	var b strings.Builder
	var quote byte // current single-line string delimiter, 0 = none
	esc := false
	// Template-literal content is emitted only on the line where the
	// template OPENS (backtick route paths like app.get(`/x`, h) are
	// same-line). Continuation lines of a multiline template are pure string
	// data - emitting them would let text inside a template match as code.
	emitTpl := false
	for i := 0; i < len(line); i++ {
		c := line[i]
		switch {
		case st.inBlock:
			if s.blockClose != "" && strings.HasPrefix(line[i:], s.blockClose) {
				st.inBlock = false
				i += len(s.blockClose) - 1
			}
		case st.inMultiStr:
			if emitTpl {
				b.WriteByte(c)
			}
			if esc {
				esc = false
			} else if c == '\\' {
				esc = true
			} else if c == s.multilineQuote {
				st.inMultiStr = false
			}
		case quote != 0:
			b.WriteByte(c)
			if esc {
				esc = false
			} else if c == '\\' {
				esc = true
			} else if c == quote {
				quote = 0
			}
		case s.multilineQuote != 0 && c == s.multilineQuote:
			st.inMultiStr = true
			emitTpl = true
			b.WriteByte(c)
		case strings.IndexByte(s.quotes, c) >= 0:
			quote = c
			b.WriteByte(c)
		case s.blockOpen != "" && strings.HasPrefix(line[i:], s.blockOpen):
			st.inBlock = true
			i += len(s.blockOpen) - 1
		default:
			if marker := matchLineMarker(line[i:], s.lineMarkers); marker != "" {
				return b.String()
			}
			b.WriteByte(c)
		}
	}
	return b.String()
}

func matchLineMarker(rest string, markers []string) string {
	for _, m := range markers {
		if strings.HasPrefix(rest, m) {
			return m
		}
	}
	return ""
}
