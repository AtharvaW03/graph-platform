// Package sqldialect decides which SQL dialect a .sql file is written in, so
// exactly one dialect extractor claims each file.
//
// The mssql and postgres extractors both walk every .sql file in a repo. Left
// alone they would both match a plain `CREATE TABLE`, emitting two nodes for
// one physical table - and the loser would be wrong about the default schema
// (T-SQL's dbo vs Postgres's public), which is part of the node key. Routing
// on content keeps SQL objects to one node each.
//
// Detection is by marker scoring, not parsing: each dialect has syntax the
// other cannot use, and the file with more of one dialect's markers is that
// dialect. A file with no markers either way is Unknown - it stays with the
// mssql extractor, which has always had it.
package sqldialect

import (
	"fmt"
	"regexp"
	"strings"
)

// Dialect identifies which extractor owns a file.
type Dialect int

const (
	// Unknown is dialect-neutral DDL: plain CREATE TABLE with no syntax
	// specific to either engine. Not a failure - most schema-only files
	// look like this, and they route to mssql to preserve prior behavior.
	Unknown Dialect = iota
	MSSQL
	Postgres
)

func (d Dialect) String() string {
	switch d {
	case MSSQL:
		return "mssql"
	case Postgres:
		return "postgres"
	}
	return "unknown"
}

// marker is one piece of dialect evidence. weight separates syntax that is
// merely idiomatic (1) from syntax the other engine cannot parse at all (3),
// so one dollar-quoted body outvotes a handful of incidental keyword hits.
type marker struct {
	re     *regexp.Regexp
	weight int
}

var postgresMarkers = []marker{
	// Dollar-quoted string bodies. No T-SQL equivalent, and the single
	// strongest signal a file is Postgres.
	{regexp.MustCompile(`\$\$|\$[A-Za-z_][A-Za-z0-9_]*\$`), 3},
	{regexp.MustCompile(`(?i)\bLANGUAGE\s+(?:plpgsql|sql|plpython3u|plperl|c)\b`), 3},
	{regexp.MustCompile(`(?i)\bCREATE\s+MATERIALIZED\s+VIEW\b`), 3},
	{regexp.MustCompile(`(?i)\bEXECUTE\s+(?:FUNCTION|PROCEDURE)\b`), 3},
	{regexp.MustCompile(`(?i)\bCREATE\s+EXTENSION\b`), 3},
	{regexp.MustCompile(`(?i)\bSET\s+search_path\b`), 3},
	{regexp.MustCompile(`(?i)\bON\s+CONFLICT\b`), 3},
	{regexp.MustCompile(`(?i)\bRETURNS\s+(?:TABLE|SETOF|trigger)\b`), 2},
	{regexp.MustCompile(`(?i)\b(?:big|small)?serial\b`), 2},
	{regexp.MustCompile(`(?i)\bCREATE\s+OR\s+REPLACE\b`), 2},
	{regexp.MustCompile(`(?i)\b(?:jsonb|tsvector|bytea|int4range|citext)\b`), 2},
	// PostgreSQL cast syntax. Excludes the ::= of other grammars by
	// requiring an identifier or ) on the left and a type word on the right.
	{regexp.MustCompile(`[A-Za-z0-9_)]::[A-Za-z_]`), 2},
	{regexp.MustCompile(`(?i)\bCOMMENT\s+ON\s+(?:TABLE|COLUMN|FUNCTION|SCHEMA)\b`), 2},
	{regexp.MustCompile(`(?i)\bALTER\s+TABLE\s+ONLY\b`), 2},
	{regexp.MustCompile(`(?i)\b(?:TEXT|VARCHAR)\s*\[\s*\]`), 2},
	{regexp.MustCompile(`(?i)\bOWNER\s+TO\b`), 1},
	{regexp.MustCompile(`(?i)\bCREATE\s+SEQUENCE\b`), 1},
	{regexp.MustCompile(`(?i)\bIF\s+NOT\s+EXISTS\b`), 1},
	{regexp.MustCompile(`(?i)\bnow\(\)|\bgen_random_uuid\(\)`), 1},
}

var mssqlMarkers = []marker{
	// The GO batch separator, alone on its line. Not SQL at all - it is a
	// client directive only SQL Server tooling understands.
	{regexp.MustCompile(`(?im)^\s*GO\s*;?\s*$`), 3},
	{regexp.MustCompile(`(?i)\bCREATE\s+OR\s+ALTER\b`), 3},
	{regexp.MustCompile(`(?i)\bsp_executesql\b`), 3},
	{regexp.MustCompile(`(?i)\bSET\s+NOCOUNT\s+ON\b`), 3},
	{regexp.MustCompile(`(?i)\bIDENTITY\s*\(\s*\d+\s*,\s*\d+\s*\)`), 3},
	{regexp.MustCompile(`(?i)\b(?:NVARCHAR|UNIQUEIDENTIFIER|DATETIME2|NCHAR|SMALLDATETIME|MONEY)\b`), 3},
	// Bracket-quoted identifiers. Requires a word inside so it cannot match
	// Postgres's text[] array suffix.
	{regexp.MustCompile(`\[[A-Za-z_][A-Za-z0-9_ ]*\]`), 3},
	{regexp.MustCompile(`(?i)\bVARCHAR\s*\(\s*MAX\s*\)`), 3},
	// T-SQL local variables and parameters.
	{regexp.MustCompile(`@[A-Za-z_][A-Za-z0-9_]*`), 2},
	{regexp.MustCompile(`(?i)\bBEGIN\s+TRAN(?:SACTION)?\b`), 2},
	{regexp.MustCompile(`(?i)\bEXEC(?:UTE)?\s+[A-Za-z_\[]`), 2},
	{regexp.MustCompile(`(?i)\bdbo\s*\.`), 2},
	{regexp.MustCompile(`(?i)\bWITH\s*\(\s*NOLOCK\s*\)`), 2},
	{regexp.MustCompile(`(?i)\bTOP\s+\(?\d+\)?\b`), 1},
	{regexp.MustCompile(`(?i)\bISNULL\s*\(|\bGETDATE\s*\(\)|\bSCOPE_IDENTITY\s*\(\)`), 1},
	{regexp.MustCompile(`(?i)\bCLUSTERED\b`), 1},
}

// Parse maps a config value to a Dialect. An empty string is Unknown, which
// callers treat as "use the built-in default".
func Parse(s string) (Dialect, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "":
		return Unknown, nil
	case "mssql", "sqlserver", "tsql":
		return MSSQL, nil
	case "postgres", "postgresql", "pg":
		return Postgres, nil
	}
	return Unknown, fmt.Errorf("unknown sql dialect %q (want mssql or postgres)", s)
}

// Owner decides which extractor owns a file: the detected dialect when there
// is one, and def otherwise.
//
// Most committed DDL is dialect-neutral - a plain CREATE TABLE could be
// either engine - so the fallback is what a single-engine org actually runs
// on. It defaults to MSSQL because that is the dialect this platform
// extracted before Postgres support existed, and silently re-keying every
// neutral file from dbo to public would be a migration, not a default.
func Owner(content string, def Dialect) Dialect {
	if d := Detect(content); d != Unknown {
		return d
	}
	if def == Unknown {
		return MSSQL
	}
	return def
}

// Detect scores content against both marker sets and returns the winner.
// A tie - including no markers at all - is Unknown.
func Detect(content string) Dialect {
	pg, ms := score(content, postgresMarkers), score(content, mssqlMarkers)
	switch {
	case pg > ms:
		return Postgres
	case ms > pg:
		return MSSQL
	default:
		return Unknown
	}
}

// score sums the weights of every marker that appears at least once. Markers
// count once regardless of how often they repeat, so a file with one heavily
// used construct cannot outweigh a file with broad evidence of the other
// dialect.
func score(content string, markers []marker) int {
	total := 0
	for _, m := range markers {
		if m.re.MatchString(content) {
			total += m.weight
		}
	}
	return total
}
