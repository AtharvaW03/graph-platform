package sqldialect

import "testing"

func TestDetect(t *testing.T) {
	cases := []struct {
		name string
		sql  string
		want Dialect
	}{
		{
			name: "postgres function with dollar-quoted body",
			sql: `CREATE OR REPLACE FUNCTION billing.settle(p_id bigint)
RETURNS void LANGUAGE plpgsql AS $$
BEGIN
  UPDATE billing.invoices SET paid = true WHERE id = p_id;
END;
$$;`,
			want: Postgres,
		},
		{
			name: "postgres materialized view",
			sql:  "CREATE MATERIALIZED VIEW mv AS SELECT id::text FROM t;",
			want: Postgres,
		},
		{
			name: "tsql batch with brackets and GO",
			sql: `CREATE PROCEDURE [dbo].[usp_Report] AS
BEGIN
    SET NOCOUNT ON;
    SELECT TOP 10 * FROM [dbo].[Orders];
END
GO`,
			want: MSSQL,
		},
		{
			name: "tsql types and variables",
			sql: `CREATE TABLE Customers (
    Id UNIQUEIDENTIFIER PRIMARY KEY,
    Name NVARCHAR(200)
);`,
			want: MSSQL,
		},
		{
			name: "dialect-neutral DDL stays unknown so mssql keeps it",
			sql: `CREATE TABLE orders (
    id INT PRIMARY KEY,
    qty INT NOT NULL
);`,
			want: Unknown,
		},
		{
			name: "empty file",
			sql:  "",
			want: Unknown,
		},
		{
			// A Postgres array type must not read as a T-SQL bracket-quoted
			// identifier, or every array column would drag the file to MSSQL.
			name: "postgres array column is not a bracketed identifier",
			sql:  "CREATE TABLE t (tags text[], meta jsonb);",
			want: Postgres,
		},
		{
			// Postgres wins on strength, not count: the file mentions dbo
			// (2) but has a dollar-quoted body plus plpgsql (6).
			name: "mixed markers resolve to the stronger dialect",
			sql: `-- ported from dbo.legacy_settle
CREATE FUNCTION settle() RETURNS void LANGUAGE plpgsql AS $$ BEGIN END; $$;`,
			want: Postgres,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Detect(tc.sql); got != tc.want {
				t.Errorf("Detect() = %s, want %s (pg=%d mssql=%d)",
					got, tc.want, score(tc.sql, postgresMarkers), score(tc.sql, mssqlMarkers))
			}
		})
	}
}

func TestDialectString(t *testing.T) {
	for d, want := range map[Dialect]string{
		Unknown:  "unknown",
		MSSQL:    "mssql",
		Postgres: "postgres",
	} {
		if got := d.String(); got != want {
			t.Errorf("Dialect(%d).String() = %q, want %q", d, got, want)
		}
	}
}

func TestParse(t *testing.T) {
	ok := map[string]Dialect{
		"":           Unknown,
		"mssql":      MSSQL,
		"SQLServer":  MSSQL,
		"tsql":       MSSQL,
		"postgres":   Postgres,
		"PostgreSQL": Postgres,
		" pg ":       Postgres,
	}
	for in, want := range ok {
		got, err := Parse(in)
		if err != nil || got != want {
			t.Errorf("Parse(%q) = %s, %v; want %s, nil", in, got, err, want)
		}
	}
	if _, err := Parse("oracle"); err == nil {
		t.Error("Parse(\"oracle\") should error so a typo fails config validation, not silently picks a dialect")
	}
}

func TestOwnerResolvesNeutralFilesToTheDefault(t *testing.T) {
	neutral := "CREATE TABLE orders (id INT PRIMARY KEY);"
	pgFile := "CREATE FUNCTION f() RETURNS void LANGUAGE plpgsql AS $$ BEGIN END; $$;"
	tsqlFile := "CREATE TABLE [dbo].[Orders] (Id UNIQUEIDENTIFIER);"

	// Unset default keeps historical behavior: neutral files are mssql's.
	if got := Owner(neutral, Unknown); got != MSSQL {
		t.Errorf("Owner(neutral, Unknown) = %s, want mssql", got)
	}
	if got := Owner(neutral, Postgres); got != Postgres {
		t.Errorf("Owner(neutral, Postgres) = %s, want postgres", got)
	}
	// A detected dialect always beats the default - the fallback must never
	// override real evidence.
	if got := Owner(pgFile, MSSQL); got != Postgres {
		t.Errorf("Owner(postgres file, MSSQL default) = %s, want postgres", got)
	}
	if got := Owner(tsqlFile, Postgres); got != MSSQL {
		t.Errorf("Owner(tsql file, Postgres default) = %s, want mssql", got)
	}
}

// Exactly one extractor must own each file, under every default. Both
// claiming produces duplicate nodes for one object; neither claiming loses
// the file silently.
func TestOwnerIsTotalAndUnambiguous(t *testing.T) {
	files := []string{
		"",
		"CREATE TABLE orders (id INT);",
		"CREATE FUNCTION f() RETURNS void LANGUAGE plpgsql AS $$ BEGIN END; $$;",
		"CREATE PROCEDURE [dbo].[p] AS BEGIN SET NOCOUNT ON; END\nGO",
		"-- just a comment",
		"CREATE MATERIALIZED VIEW m AS SELECT 1;",
	}
	for _, def := range []Dialect{Unknown, MSSQL, Postgres} {
		for _, f := range files {
			got := Owner(f, def)
			if got != MSSQL && got != Postgres {
				t.Errorf("Owner(%q, %s) = %s; every file must have exactly one owner", f, def, got)
			}
		}
	}
}
