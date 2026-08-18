package postgres

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"a1-knowledge-graph/internal/extract/sqldialect"
)

// fixtureSQL is a realistic Postgres schema file: schema, tables with both
// inline and constraint-added foreign keys, a materialized view, a plpgsql
// function, a procedure, and a trigger.
const fixtureSQL = `-- billing schema
CREATE SCHEMA IF NOT EXISTS billing;

CREATE TABLE billing.customers (
    id bigserial PRIMARY KEY,
    name text NOT NULL
);

CREATE TABLE billing.invoices (
    id bigserial PRIMARY KEY,
    customer_id bigint REFERENCES billing.customers,
    paid boolean DEFAULT false
);

-- unqualified: resolves to public, not dbo
CREATE TABLE ledger (
    id bigserial PRIMARY KEY
);

CREATE MATERIALIZED VIEW billing.mv_open AS
    SELECT i.id
      FROM billing.invoices i
      JOIN billing.customers c ON c.id = i.customer_id
     WHERE i.paid = false;

CREATE OR REPLACE FUNCTION billing.settle(p_id bigint)
RETURNS void
LANGUAGE plpgsql
AS $$
DECLARE
    v_note text := 'it''s settled; really';
BEGIN
    UPDATE billing.invoices SET paid = true WHERE id = p_id;
    INSERT INTO billing.audit (note) VALUES (v_note);
    DELETE FROM billing.staging WHERE id = p_id;
    PERFORM billing.notify_customer(p_id);
    -- INSERT INTO billing.never_touched VALUES (1);
END;
$$;

CREATE PROCEDURE billing.nightly()
LANGUAGE plpgsql
AS $$
BEGIN
    CALL billing.settle_all();
    SELECT count(*) FROM billing.invoices;
END;
$$;

CREATE TRIGGER trg_invoices
    AFTER INSERT ON billing.invoices
    FOR EACH ROW EXECUTE FUNCTION billing.settle();

ALTER TABLE ONLY billing.invoices
    ADD CONSTRAINT fk_cust FOREIGN KEY (customer_id) REFERENCES billing.customers(id);
`

type sqlGraph struct {
	nodes map[string]string          // id -> type
	meta  map[string]map[string]any  // id -> metadata
	edges map[string]map[string]bool // relation -> "source|target"
}

func (g sqlGraph) hasEdge(relation, source, target string) bool {
	return g.edges[relation][source+"|"+target]
}

func runExtract(t *testing.T) sqlGraph {
	t.Helper()
	return runExtractSQL(t, fixtureSQL)
}

// runExtractSQL writes sql as the sole schema.sql in a fresh temp repo and
// extracts it.
func runExtractSQL(t *testing.T, sql string) sqlGraph {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "schema.sql"), []byte(sql), 0o644); err != nil {
		t.Fatal(err)
	}
	frag, err := New().Extract(context.Background(), dir, "pg-repo")
	if err != nil {
		t.Fatal(err)
	}
	if err := frag.Validate(); err != nil {
		t.Fatalf("fragment failed validation: %v", err)
	}
	g := sqlGraph{
		nodes: map[string]string{},
		meta:  map[string]map[string]any{},
		edges: map[string]map[string]bool{},
	}
	for _, n := range frag.Nodes {
		g.nodes[n.ID] = n.Type
		g.meta[n.ID] = n.Metadata
	}
	for _, e := range frag.Edges {
		if g.edges[e.Relation] == nil {
			g.edges[e.Relation] = map[string]bool{}
		}
		g.edges[e.Relation][e.Source+"|"+e.Target] = true
	}
	return g
}

func TestObjectsExtracted(t *testing.T) {
	g := runExtract(t)
	want := map[string]string{
		"sql::schema::billing":                   "sql_schema",
		"sql::sql_table::billing.customers":      "sql_table",
		"sql::sql_table::billing.invoices":       "sql_table",
		"sql::sql_view::billing.mv_open":         "sql_view",
		"sql::sql_function::billing.settle":      "sql_function",
		"sql::sql_procedure::billing.nightly":    "sql_procedure",
		"sql::sql_trigger::billing.trg_invoices": "sql_trigger",
	}
	for id, kind := range want {
		if got := g.nodes[id]; got != kind {
			t.Errorf("node %s = %q, want %q", id, got, kind)
		}
	}
}

// The default schema is the single most important difference from the mssql
// extractor: an unqualified Postgres object is public, never dbo.
func TestUnqualifiedObjectUsesPublicNotDbo(t *testing.T) {
	g := runExtract(t)
	if got := g.nodes["sql::sql_table::public.ledger"]; got != "sql_table" {
		t.Errorf("unqualified table = %q at public.ledger, want sql_table", got)
	}
	for id := range g.nodes {
		if strings.Contains(id, "::dbo.") || id == "sql::schema::dbo" {
			t.Errorf("emitted a T-SQL dbo node from a Postgres file: %s", id)
		}
	}
}

func TestMaterializedViewFlagged(t *testing.T) {
	g := runExtract(t)
	meta := g.meta["sql::sql_view::billing.mv_open"]
	if meta["materialized"] != true {
		t.Errorf("materialized view metadata = %v, want materialized:true", meta)
	}
	// It reads through the same relation a plain view does, so the SQL
	// object browser needs no special case.
	if !g.hasEdge("depends_on_object", "sql::sql_view::billing.mv_open", "sql::sql_table::billing.invoices") {
		t.Error("materialized view missing depends_on_object to billing.invoices")
	}
	if !g.hasEdge("depends_on_object", "sql::sql_view::billing.mv_open", "sql::sql_table::billing.customers") {
		t.Error("materialized view missing depends_on_object to billing.customers (JOIN)")
	}
}

// A Postgres trigger name is not schema-qualified; it belongs to its table's
// schema. Keying it to public would split it from the table it fires on.
func TestTriggerTakesSchemaFromItsTable(t *testing.T) {
	g := runExtract(t)
	trigger := "sql::sql_trigger::billing.trg_invoices"
	if g.nodes["sql::sql_trigger::public.trg_invoices"] != "" {
		t.Error("trigger keyed to public; want its table's schema (billing)")
	}
	if !g.hasEdge("triggers_on", trigger, "sql::sql_table::billing.invoices") {
		t.Error("missing triggers_on edge")
	}
	if !g.hasEdge("in_schema", trigger, "sql::schema::billing") {
		t.Error("missing in_schema edge to billing")
	}
	if !g.hasEdge("depends_on_object", trigger, "sql::sql_function::billing.settle") {
		t.Error("missing EXECUTE FUNCTION edge from trigger to billing.settle")
	}
}

func TestFunctionBodyReadsAndWrites(t *testing.T) {
	g := runExtract(t)
	fn := "sql::sql_function::billing.settle"
	for _, target := range []string{
		"sql::sql_table::billing.invoices", // UPDATE
		"sql::sql_table::billing.audit",    // INSERT INTO
		"sql::sql_table::billing.staging",  // DELETE FROM
	} {
		if !g.hasEdge("writes_table", fn, target) {
			t.Errorf("missing writes_table %s -> %s", fn, target)
		}
	}
	// PERFORM invokes a function, not a table.
	if !g.hasEdge("depends_on_object", fn, "sql::sql_function::billing.notify_customer") {
		t.Error("missing PERFORM edge to billing.notify_customer")
	}
	// DELETE FROM is a write only; it must not also register as a read.
	if g.hasEdge("reads_table", fn, "sql::sql_table::billing.staging") {
		t.Error("DELETE FROM produced a reads_table edge as well as a write")
	}
}

func TestProcedureCallAndRead(t *testing.T) {
	g := runExtract(t)
	proc := "sql::sql_procedure::billing.nightly"
	if !g.hasEdge("depends_on_object", proc, "sql::sql_procedure::billing.settle_all") {
		t.Error("missing CALL edge to billing.settle_all")
	}
	if !g.hasEdge("reads_table", proc, "sql::sql_table::billing.invoices") {
		t.Error("missing reads_table edge to billing.invoices")
	}
}

// Inline REFERENCES with no column list is valid Postgres (the target's
// primary key is implied) and is the form the mssql extractor cannot see,
// because it requires a trailing "(".
func TestInlineForeignKeyWithoutColumnList(t *testing.T) {
	g := runExtract(t)
	if !g.hasEdge("depends_on_object", "sql::sql_table::billing.invoices", "sql::sql_table::billing.customers") {
		t.Error("missing inline REFERENCES edge (no column list form)")
	}
}

// ALTER TABLE ... ADD CONSTRAINT is how Postgres migrations add foreign keys,
// and is a documented non-goal of the mssql extractor.
func TestAlterTableAddsForeignKeyEdge(t *testing.T) {
	g := runExtractSQL(t, `CREATE EXTENSION IF NOT EXISTS pgcrypto;

ALTER TABLE ONLY shop.orders
    ADD CONSTRAINT fk_customer FOREIGN KEY (customer_id) REFERENCES shop.customers(id);
`)
	if !g.hasEdge("depends_on_object", "sql::sql_table::shop.orders", "sql::sql_table::shop.customers") {
		t.Fatalf("ALTER TABLE FK produced no edge; edges: %v", g.edges)
	}
	// Both ends are forward-declared so the edge resolves against tables
	// defined in another file or another repo.
	if g.nodes["sql::sql_table::shop.orders"] != "sql_table" ||
		g.nodes["sql::sql_table::shop.customers"] != "sql_table" {
		t.Error("ALTER TABLE FK did not forward-declare both tables")
	}
}

// A CTE is referenced exactly like a table. Without suppression every WITH
// query invents table nodes that do not exist.
func TestCTENamesAreNotTables(t *testing.T) {
	g := runExtractSQL(t, `CREATE OR REPLACE VIEW rpt.summary AS
WITH recent AS (
    SELECT id FROM rpt.events WHERE at > now()
), rolled AS (
    SELECT id FROM recent
)
SELECT * FROM rolled JOIN recent ON recent.id = rolled.id;
`)
	for _, ghost := range []string{"sql::sql_table::public.recent", "sql::sql_table::public.rolled"} {
		if g.nodes[ghost] != "" {
			t.Errorf("CTE became a table node: %s", ghost)
		}
	}
	if !g.hasEdge("depends_on_object", "sql::sql_view::rpt.summary", "sql::sql_table::rpt.events") {
		t.Error("real table inside the CTE was not captured")
	}
}

// Set-returning functions sit in FROM position but are not tables.
func TestSetReturningFunctionsAreNotTables(t *testing.T) {
	g := runExtractSQL(t, `CREATE OR REPLACE VIEW app.expanded AS
    SELECT t.id::text, x
      FROM app.tags t, unnest(t.labels) AS x
      JOIN generate_series(1, 10) g ON true
     WHERE EXISTS (SELECT 1 FROM app.audit a WHERE a.id = t.id);
`)
	for _, ghost := range []string{"sql::sql_table::public.unnest", "sql::sql_table::public.generate_series"} {
		if g.nodes[ghost] != "" {
			t.Errorf("set-returning function became a table node: %s", ghost)
		}
	}
	if !g.hasEdge("depends_on_object", "sql::sql_view::app.expanded", "sql::sql_table::app.tags") {
		t.Error("real table app.tags was not captured")
	}
	if !g.hasEdge("depends_on_object", "sql::sql_view::app.expanded", "sql::sql_table::app.audit") {
		t.Error("table inside the EXISTS subquery was not captured")
	}
}

// Commented-out SQL must never enter the graph - the same rule the route
// extractor follows.
func TestCommentedSQLIsIgnored(t *testing.T) {
	g := runExtractSQL(t, `CREATE OR REPLACE FUNCTION ops.run() RETURNS void LANGUAGE plpgsql AS $$
BEGIN
    -- INSERT INTO ops.line_ghost VALUES (1);
    /* INSERT INTO ops.block_ghost VALUES (2);
       DELETE FROM ops.nested_ghost; */
    INSERT INTO ops.real_table VALUES (3);
END;
$$;
`)
	for _, ghost := range []string{
		"sql::sql_table::ops.line_ghost",
		"sql::sql_table::ops.block_ghost",
		"sql::sql_table::ops.nested_ghost",
	} {
		if g.nodes[ghost] != "" {
			t.Errorf("commented-out SQL reached the graph: %s", ghost)
		}
	}
	if !g.hasEdge("writes_table", "sql::sql_function::ops.run", "sql::sql_table::ops.real_table") {
		t.Error("real INSERT next to the comments was lost")
	}
}

// A CREATE inside a dollar-quoted body is a string, not a new statement. If
// it split the file, the enclosing function would lose the rest of its body.
func TestCreateInsideDollarQuotedBodyDoesNotSplit(t *testing.T) {
	g := runExtractSQL(t, `CREATE OR REPLACE FUNCTION ops.bootstrap() RETURNS void LANGUAGE plpgsql AS $$
BEGIN
    EXECUTE 'CREATE TABLE ops.temp_scratch (id int)';
    INSERT INTO ops.after_the_create VALUES (1);
END;
$$;
`)
	if g.nodes["sql::sql_table::ops.temp_scratch"] != "" {
		t.Error("a CREATE TABLE inside a string literal became a real table node")
	}
	if !g.hasEdge("writes_table", "sql::sql_function::ops.bootstrap", "sql::sql_table::ops.after_the_create") {
		t.Error("body after the embedded CREATE was lost - the statement split early")
	}
}

// Dollar quoting exists so bodies need not escape apostrophes. A masker that
// treated the loose quote as a string start would swallow the rest of the
// file.
func TestUnescapedApostropheInDollarBody(t *testing.T) {
	g := runExtractSQL(t, `CREATE FUNCTION ops.note() RETURNS void LANGUAGE plpgsql AS $$
BEGIN
    RAISE NOTICE 'it won't matter';
    INSERT INTO ops.still_seen VALUES (1);
END;
$$;

CREATE TABLE ops.later_table (id int);
`)
	if !g.hasEdge("writes_table", "sql::sql_function::ops.note", "sql::sql_table::ops.still_seen") {
		t.Error("statement after an unescaped apostrophe was lost")
	}
	if g.nodes["sql::sql_table::ops.later_table"] != "sql_table" {
		t.Error("object after the function was lost - masking ran past the body")
	}
}

// Custom $tag$ delimiters are as common as $$ in generated migrations.
func TestTaggedDollarQuoting(t *testing.T) {
	g := runExtractSQL(t, `CREATE FUNCTION ops.tagged() RETURNS void LANGUAGE plpgsql AS $body$
BEGIN
    INSERT INTO ops.tagged_target VALUES (1);
END;
$body$;

CREATE TABLE ops.after_tag (id int);
`)
	if !g.hasEdge("writes_table", "sql::sql_function::ops.tagged", "sql::sql_table::ops.tagged_target") {
		t.Error("body inside $body$ delimiters was not scanned")
	}
	if g.nodes["sql::sql_table::ops.after_tag"] != "sql_table" {
		t.Error("object after a $tag$ body was lost")
	}
}

// A statement ends at its own semicolon. Without that bound the last object
// in a file absorbs every trailing statement's tables.
func TestStatementDoesNotBleedPastSemicolon(t *testing.T) {
	g := runExtractSQL(t, `CREATE MATERIALIZED VIEW rpt.v AS SELECT id FROM rpt.source;

INSERT INTO rpt.seed_data (id) VALUES (1);
SELECT * FROM rpt.unrelated;
`)
	if !g.hasEdge("depends_on_object", "sql::sql_view::rpt.v", "sql::sql_table::rpt.source") {
		t.Error("view lost its real dependency")
	}
	if g.hasEdge("depends_on_object", "sql::sql_view::rpt.v", "sql::sql_table::rpt.unrelated") {
		t.Error("view absorbed a statement that came after its semicolon")
	}
}

// Routing: a T-SQL file handed to this extractor produces nothing, so the two
// extractors never both claim one file.
func TestTSQLFileIsSkipped(t *testing.T) {
	g := runExtractSQL(t, `CREATE PROCEDURE [dbo].[usp_Report] AS
BEGIN
    SET NOCOUNT ON;
    SELECT TOP 10 * FROM [dbo].[Orders];
END
GO`)
	if len(g.nodes) != 0 {
		t.Errorf("postgres extractor claimed a T-SQL file: %v", g.nodes)
	}
}

// Quoted identifiers keep their spelling and still resolve.
func TestQuotedIdentifiers(t *testing.T) {
	g := runExtractSQL(t, `CREATE TABLE "Billing"."Invoice_Lines" (
    id bigserial PRIMARY KEY
);

CREATE VIEW "Billing"."V_Lines" AS SELECT id FROM "Billing"."Invoice_Lines";
`)
	if g.nodes[`sql::sql_table::Billing.Invoice_Lines`] != "sql_table" {
		t.Errorf("quoted identifier table not extracted; nodes: %v", g.nodes)
	}
	if !g.hasEdge("depends_on_object", "sql::sql_view::Billing.V_Lines", "sql::sql_table::Billing.Invoice_Lines") {
		t.Error("quoted identifiers did not resolve across a view dependency")
	}
}

// Non-.sql files and oversized files are skipped without error.
func TestSkipsNonSQLFiles(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "notes.md"), []byte("CREATE TABLE x (id int); -- $$ plpgsql"), 0o644); err != nil {
		t.Fatal(err)
	}
	frag, err := New().Extract(context.Background(), dir, "pg-repo")
	if err != nil {
		t.Fatal(err)
	}
	if !frag.Empty() {
		t.Errorf("extracted from a non-.sql file: %v", frag.Nodes)
	}
}

// One foreign key stated twice - inline on the column and again as a named
// constraint in a later migration - is one relationship, not two edges.
func TestDuplicateForeignKeyEmitsOneEdge(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("001_create.sql", `CREATE TABLE shop.orders (
    id bigserial PRIMARY KEY,
    customer_id bigint REFERENCES shop.customers
);`)
	write("002_constraint.sql", `ALTER TABLE ONLY shop.orders
    ADD CONSTRAINT fk_customer FOREIGN KEY (customer_id) REFERENCES shop.customers(id);`)

	ex := New()
	ex.DefaultDialect = sqldialect.Postgres
	frag, err := ex.Extract(context.Background(), dir, "shop-db")
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, e := range frag.Edges {
		if e.Relation == "depends_on_object" &&
			e.Source == "sql::sql_table::shop.orders" &&
			e.Target == "sql::sql_table::shop.customers" {
			n++
		}
	}
	if n != 1 {
		t.Errorf("one foreign key produced %d edges, want 1", n)
	}
}
