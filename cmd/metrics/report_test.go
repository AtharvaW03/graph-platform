package main

import "testing"

func TestShare(t *testing.T) {
	cases := []struct {
		part, total int
		want        float64
	}{
		{0, 0, 0}, // no divide-by-zero
		{1, 0, 0}, // guarded even with a nonzero part
		{1, 2, 0.5},
		{3, 4, 0.75},
		{5, 5, 1.0},
	}
	for _, c := range cases {
		if got := share(c.part, c.total); got != c.want {
			t.Errorf("share(%d, %d) = %v, want %v", c.part, c.total, got, c.want)
		}
	}
}

func TestNormalizeTier(t *testing.T) {
	cases := map[string]string{
		"EXTRACTED": "EXTRACTED",
		"INFERRED":  "INFERRED",
		"AMBIGUOUS": "AMBIGUOUS",
		"":          "UNLABELED", // graphify never wrote a confidence
		"MAYBE":     "UNLABELED", // unknown value must not create a stray tier
		"extracted": "UNLABELED", // case-sensitive: stored values are upper
	}
	for in, want := range cases {
		if got := normalizeTier(in); got != want {
			t.Errorf("normalizeTier(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestHumanInt(t *testing.T) {
	cases := map[int]string{
		0:       "0",
		42:      "42",
		999:     "999",
		1000:    "1,000",
		674123:  "674,123",
		1000000: "1,000,000",
		-1500:   "-1,500",
	}
	for in, want := range cases {
		if got := humanInt(in); got != want {
			t.Errorf("humanInt(%d) = %q, want %q", in, got, want)
		}
	}
}

func TestAsInt(t *testing.T) {
	// Neo4j's driver returns counts as int64; the report must coerce cleanly.
	cases := []struct {
		in   any
		want int
	}{
		{int64(5), 5},
		{int(7), 7},
		{float64(9), 9},
		{nil, 0},
		{"nope", 0},
	}
	for _, c := range cases {
		if got := asInt(c.in); got != c.want {
			t.Errorf("asInt(%v) = %d, want %d", c.in, got, c.want)
		}
	}
}
