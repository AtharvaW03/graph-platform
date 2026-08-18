package postgres

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"a1-knowledge-graph/internal/extract"
	"a1-knowledge-graph/internal/extract/mssql"
	"a1-knowledge-graph/internal/extract/sqldialect"
)

// The two SQL extractors run side by side over the same checkout. Each .sql
// file must be claimed by exactly one of them: both claiming means two nodes
// for one physical object (and they disagree about the default schema, which
// is part of the node key), neither claiming loses the file silently.
func TestExactlyOneExtractorClaimsEachFile(t *testing.T) {
	files := map[string]string{
		"pg_functions.sql": `CREATE OR REPLACE FUNCTION billing.settle(p_id bigint)
RETURNS void LANGUAGE plpgsql AS $$
BEGIN
    UPDATE billing.invoices SET paid = true WHERE id = p_id;
END;
$$;`,
		"tsql_procs.sql": `CREATE OR ALTER PROCEDURE [dbo].[usp_Report] AS
BEGIN
    SET NOCOUNT ON;
    SELECT TOP 10 * FROM [dbo].[Orders];
END
GO`,
		"neutral.sql": `CREATE TABLE orders (
    id INT PRIMARY KEY,
    qty INT NOT NULL
);`,
	}

	for _, def := range []sqldialect.Dialect{sqldialect.Unknown, sqldialect.MSSQL, sqldialect.Postgres} {
		dir := t.TempDir()
		for name, body := range files {
			if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
		}

		pg := New()
		pg.DefaultDialect = def
		ms := mssql.New()
		ms.DefaultDialect = def

		pgFrag := mustExtract(t, pg, dir)
		msFrag := mustExtract(t, ms, dir)

		pgIDs := nodeIDs(pgFrag)
		msIDs := nodeIDs(msFrag)
		for id := range pgIDs {
			if msIDs[id] {
				t.Errorf("default=%s: both extractors emitted %s", def, id)
			}
		}

		// The neutral file must land somewhere - and specifically with the
		// dialect the operator nominated.
		wantPublic := def == sqldialect.Postgres
		if got := pgIDs["sql::sql_table::public.orders"]; got != wantPublic {
			t.Errorf("default=%s: postgres claimed public.orders = %v, want %v", def, got, wantPublic)
		}
		if got := msIDs["sql::sql_table::dbo.orders"]; got == wantPublic {
			t.Errorf("default=%s: mssql claimed dbo.orders = %v, want %v", def, got, !wantPublic)
		}

		// Each dialect's own file always goes to its own extractor,
		// whatever the default.
		if !pgIDs["sql::sql_function::billing.settle"] {
			t.Errorf("default=%s: postgres extractor lost a plpgsql function", def)
		}
		if !msIDs["sql::sql_procedure::dbo.usp_Report"] {
			t.Errorf("default=%s: mssql extractor lost a T-SQL procedure", def)
		}
	}
}

func TestDefaultDialectClaimsNeutralFile(t *testing.T) {
	dir := t.TempDir()
	neutral := "CREATE TABLE orders (id INT PRIMARY KEY, qty INT);"
	if err := os.WriteFile(filepath.Join(dir, "schema.sql"), []byte(neutral), 0o644); err != nil {
		t.Fatal(err)
	}

	off := mustExtract(t, New(), dir) // zero value: neutral files are mssql's
	if !off.Empty() {
		t.Errorf("with no default set, postgres claimed a neutral file: %v", nodeIDs(off))
	}

	on := New()
	on.DefaultDialect = sqldialect.Postgres
	frag := mustExtract(t, on, dir)
	if !nodeIDs(frag)["sql::sql_table::public.orders"] {
		t.Errorf("with default=postgres, neutral file was not claimed: %v", nodeIDs(frag))
	}
}

func mustExtract(t *testing.T, e extract.Extractor, dir string) *extract.Fragment {
	t.Helper()
	frag, err := e.Extract(context.Background(), dir, "sql-repo")
	if err != nil {
		t.Fatalf("%s: %v", e.Name(), err)
	}
	if err := frag.Validate(); err != nil {
		t.Fatalf("%s: fragment failed validation: %v", e.Name(), err)
	}
	return frag
}

func nodeIDs(f *extract.Fragment) map[string]bool {
	out := map[string]bool{}
	for _, n := range f.Nodes {
		out[n.ID] = true
	}
	return out
}
