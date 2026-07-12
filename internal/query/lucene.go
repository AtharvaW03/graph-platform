package query

import (
	"regexp"
	"strings"
)

// luceneSpecial matches every character Lucene's query parser treats as
// syntax rather than a literal: boolean operators, grouping, field/boost/
// fuzzy markers, and wildcards.
var luceneSpecial = regexp.MustCompile(`([+\-&|!(){}\[\]^"~*?:\\/])`)

// luceneEscape makes q safe to hand to db.index.fulltext.queryNodes as a
// literal search term. Without this, a user-typed `*`, `AND`, or unbalanced
// quote either turns into an unintended wildcard/boolean query or a Lucene
// parse error the caller would otherwise see as a 500.
func luceneEscape(q string) string {
	q = strings.TrimSpace(q)
	if q == "" {
		return ""
	}
	// Control characters have no place in a search term and the fulltext
	// index wouldn't match on them anyway.
	q = strings.Map(func(r rune) rune {
		if r < 32 {
			return -1
		}
		return r
	}, q)
	return luceneSpecial.ReplaceAllString(q, `\$1`)
}
