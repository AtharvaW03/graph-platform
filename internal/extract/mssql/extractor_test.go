package mssql

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const fixtureSQL = `CREATE SCHEMA trade
GO

CREATE TABLE trade.orders (
    id INT PRIMARY KEY,
    qty INT
)
GO

CREATE PROCEDURE trade.usp_cleanup AS
BEGIN
    DELETE FROM trade.orders WHERE qty = 0
END
GO

CREATE OR ALTER PROCEDURE trade.usp_report AS
BEGIN
    SELECT o.id FROM trade.orders o
    JOIN dbo.users u ON u.id = o.id
    INSERT INTO trade.audit (id) VALUES (1)
END
GO

CREATE VIEW trade.v_open AS
    SELECT id FROM trade.orders WHERE qty > 0
GO

CREATE TRIGGER trade.trg_orders ON trade.orders AFTER INSERT AS
BEGIN
    INSERT INTO trade.audit (id) SELECT id FROM inserted
END
`

type sqlGraph struct {
	nodes map[string]string          // id -> type
	edges map[string]map[string]bool // relation -> "source|target"
}

func runExtract(t *testing.T) sqlGraph {
	t.Helper()
	return runExtractSQL(t, fixtureSQL)
}

// runExtractSQL writes sql as the sole schema.sql in a fresh temp repo and
// extracts it. The per-bug tests below use this directly with minimal inline
// fixtures instead of fixtureSQL.
func runExtractSQL(t *testing.T, sql string) sqlGraph {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "schema.sql"), []byte(sql), 0o644); err != nil {
		t.Fatal(err)
	}
	frag, err := New().Extract(context.Background(), dir, "db-repo")
	if err != nil {
		t.Fatal(err)
	}
	g := sqlGraph{nodes: map[string]string{}, edges: map[string]map[string]bool{}}
	for _, n := range frag.Nodes {
		g.nodes[n.ID] = n.Type
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
	wantNodes := map[string]string{
		"sql::schema::trade":                    "sql_schema",
		"sql::sql_table::trade.orders":          "sql_table",
		"sql::sql_procedure::trade.usp_cleanup": "sql_procedure",
		"sql::sql_procedure::trade.usp_report":  "sql_procedure",
		"sql::sql_view::trade.v_open":           "sql_view",
		"sql::sql_trigger::trade.trg_orders":    "sql_trigger",
	}
	for id, typ := range wantNodes {
		if g.nodes[id] != typ {
			t.Errorf("node %s: type = %q, want %q", id, g.nodes[id], typ)
		}
	}
}

// TestDeleteIsWriteNotRead: a proc that only deletes from a table must get
// writes_table but NOT reads_table for it.
func TestDeleteIsWriteNotRead(t *testing.T) {
	g := runExtract(t)
	cleanup := "sql::sql_procedure::trade.usp_cleanup"
	orders := "sql::sql_table::trade.orders"
	if !g.edges["writes_table"][cleanup+"|"+orders] {
		t.Error("DELETE FROM did not produce writes_table edge")
	}
	if g.edges["reads_table"][cleanup+"|"+orders] {
		t.Error("DELETE FROM incorrectly counted as reads_table")
	}
}

func TestProcReadsAndWrites(t *testing.T) {
	g := runExtract(t)
	report := "sql::sql_procedure::trade.usp_report"
	for _, want := range []string{
		report + "|sql::sql_table::trade.orders",
		report + "|sql::sql_table::dbo.users",
	} {
		if !g.edges["reads_table"][want] {
			t.Errorf("missing reads_table %s; got %v", want, g.edges["reads_table"])
		}
	}
	if !g.edges["writes_table"][report+"|sql::sql_table::trade.audit"] {
		t.Errorf("missing writes_table to trade.audit; got %v", g.edges["writes_table"])
	}
}

func TestViewAndTriggerEdges(t *testing.T) {
	g := runExtract(t)
	if !g.edges["depends_on_object"]["sql::sql_view::trade.v_open|sql::sql_table::trade.orders"] {
		t.Errorf("view dependency missing; got %v", g.edges["depends_on_object"])
	}
	if !g.edges["triggers_on"]["sql::sql_trigger::trade.trg_orders|sql::sql_table::trade.orders"] {
		t.Errorf("trigger target missing; got %v", g.edges["triggers_on"])
	}
	if !g.edges["in_schema"]["sql::sql_table::trade.orders|sql::schema::trade"] {
		t.Errorf("in_schema missing; got %v", g.edges["in_schema"])
	}
}

// TestExecTargetIsProcedureNotTable: an EXEC target is a procedure, not a
// table. sp_executesql (dynamic SQL's entry point) must not create a node at
// all, since it is never a real object name.
func TestExecTargetIsProcedureNotTable(t *testing.T) {
	const sql = `CREATE PROCEDURE billing.SettleInvoice AS
BEGIN
    SELECT 1
END
GO

CREATE PROCEDURE billing.MonthlyStatement AS
BEGIN
    EXEC billing.SettleInvoice
    EXEC sp_executesql @sql
END
GO
`
	g := runExtractSQL(t, sql)
	target := "sql::sql_procedure::billing.SettleInvoice"
	source := "sql::sql_procedure::billing.MonthlyStatement"

	if got := g.nodes[target]; got != "sql_procedure" {
		t.Errorf("EXEC target node type = %q, want sql_procedure (bug: was sql_table)", got)
	}
	if !g.edges["depends_on_object"][source+"|"+target] {
		t.Errorf("missing depends_on_object %s -> %s; got %v", source, target, g.edges["depends_on_object"])
	}
	for id := range g.nodes {
		if strings.Contains(strings.ToLower(id), "sp_executesql") {
			t.Errorf("sp_executesql must never produce a node, got %s", id)
		}
	}
}

// TestTriggerHeaderUpdateNotMisreadAsTable: the trigger header
// "AFTER UPDATE\nAS" must not be misread as "UPDATE AS" (a table named AS).
func TestTriggerHeaderUpdateNotMisreadAsTable(t *testing.T) {
	const sql = `CREATE TABLE billing.Orders (id INT)
GO

CREATE TRIGGER billing.trg_orders ON billing.Orders AFTER UPDATE
AS
BEGIN
    SELECT 1
END
GO
`
	g := runExtractSQL(t, sql)
	for id := range g.nodes {
		if strings.HasSuffix(strings.ToLower(id), ".as") {
			t.Errorf("trigger header must not produce a table named AS, got node %s", id)
		}
	}
	for pair := range g.edges["writes_table"] {
		if strings.HasSuffix(strings.ToLower(pair), ".as") {
			t.Errorf("trigger header must not produce a writes_table edge to a table named AS, got %s", pair)
		}
	}
}

// TestTriggerPseudoTablesFiltered: inserted/deleted are T-SQL
// pseudo-tables, not real tables. Filtered globally, since a real table
// named inserted is vanishingly rare.
func TestTriggerPseudoTablesFiltered(t *testing.T) {
	const sql = `CREATE TABLE billing.Orders (id INT)
GO

CREATE TABLE billing.AuditLog (id INT)
GO

CREATE TRIGGER billing.trg_orders ON billing.Orders AFTER INSERT AS
BEGIN
    INSERT INTO billing.AuditLog (id) SELECT id FROM inserted
END
GO
`
	g := runExtractSQL(t, sql)
	for id := range g.nodes {
		lower := strings.ToLower(id)
		if strings.HasSuffix(lower, ".inserted") || strings.HasSuffix(lower, ".deleted") {
			t.Errorf("pseudo-table must not produce a node, got %s", id)
		}
	}
	// The blocklist must not swallow a real edge in the same body.
	trg := "sql::sql_trigger::billing.trg_orders"
	audit := "sql::sql_table::billing.AuditLog"
	if !g.edges["writes_table"][trg+"|"+audit] {
		t.Errorf("real writes_table edge to AuditLog should survive the pseudo-table filter; got %v", g.edges["writes_table"])
	}
}

// TestForeignKeyReferencesExtracted: FOREIGN KEY REFERENCES inside a
// CREATE TABLE body must produce references edges. Covers bracketed +
// schema-qualified and unbracketed + unqualified (defaults to dbo) forms.
func TestForeignKeyReferencesExtracted(t *testing.T) {
	const sql = `CREATE TABLE billing.Customers (
    CustomerID INT PRIMARY KEY
)
GO

CREATE TABLE billing.Invoices (
    InvoiceID INT PRIMARY KEY,
    CustomerID INT,
    CONSTRAINT FK_Invoices_Customers FOREIGN KEY (CustomerID)
        REFERENCES [billing].[Customers](CustomerID)
)
GO

CREATE TABLE billing.Orders (
    id INT,
    CustomerID INT,
    FOREIGN KEY (CustomerID) REFERENCES Customers(id)
)
GO
`
	g := runExtractSQL(t, sql)
	if !g.edges["depends_on_object"]["sql::sql_table::billing.Invoices|sql::sql_table::billing.Customers"] {
		t.Errorf("bracketed, schema-qualified FK missing; got %v", g.edges["depends_on_object"])
	}
	if !g.edges["depends_on_object"]["sql::sql_table::billing.Orders|sql::sql_table::dbo.Customers"] {
		t.Errorf("unbracketed, unqualified FK missing; got %v", g.edges["depends_on_object"])
	}
}

// TestALTERTableForeignKeysNotCaptured documents a known non-goal: FKs
// added via ALTER TABLE ADD CONSTRAINT are not captured - splitObjects only
// splits on CREATE statements, so a migration file containing only ALTER
// statements is never scanned.
func TestALTERTableForeignKeysNotCaptured(t *testing.T) {
	dir := t.TempDir()
	createSQL := `CREATE TABLE billing.Customers (CustomerID INT PRIMARY KEY)
GO

CREATE TABLE billing.Orders (id INT, CustomerID INT)
GO
`
	// Realistic separate migration file: only an ALTER, no CREATE TABLE hit
	// to anchor it to - this is what actually makes it invisible, not just
	// proximity to its own table's CREATE statement.
	alterSQL := `ALTER TABLE billing.Orders ADD CONSTRAINT FK_Orders_Customers
    FOREIGN KEY (CustomerID) REFERENCES billing.Customers(CustomerID)
GO
`
	if err := os.WriteFile(filepath.Join(dir, "001_create.sql"), []byte(createSQL), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "002_alter.sql"), []byte(alterSQL), 0o644); err != nil {
		t.Fatal(err)
	}
	frag, err := New().Extract(context.Background(), dir, "db-repo")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range frag.Edges {
		if e.Relation == "depends_on_object" &&
			e.Source == "sql::sql_table::billing.Orders" &&
			e.Target == "sql::sql_table::billing.Customers" {
			t.Error("ALTER TABLE ADD CONSTRAINT FK should not be captured this batch (documented non-goal)")
		}
	}
}
