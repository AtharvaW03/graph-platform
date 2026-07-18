import { useEffect, useState, type FormEvent } from "react";
import { useSearchParams } from "react-router-dom";
import { api } from "../api";
import { useAsync } from "../hooks/useAsync";
import { useRepoScope } from "../context/RepoScope";
import { StatusBox } from "../components/StatusBox";
import { DataTable, LabelBadges } from "../components/DataTable";
import { FeedbackWidget } from "../components/FeedbackWidget";
import { RepoPicker } from "../components/RepoPicker";
import { Button, Card, Input, PageHeader, Segmented } from "../components/ui";
import type { SearchResult, SymbolResult } from "../types";

type Mode = "fuzzy" | "symbol";

export function Search() {
  const [params, setParams] = useSearchParams();
  const [q, setQ] = useState(params.get("q") ?? "");
  const [mode, setMode] = useState<Mode>(
    params.get("mode") === "symbol" ? "symbol" : "fuzzy",
  );
  const [ratedQuery, setRatedQuery] = useState("");
  const { selected, setSelected } = useRepoScope();

  const fuzzy = useAsync<SearchResult[]>();
  const symbol = useAsync<SymbolResult[]>();
  const active = mode === "fuzzy" ? fuzzy : symbol;

  const runQuery = (m: Mode, query: string) => {
    setRatedQuery(query);
    if (m === "fuzzy") fuzzy.run(() => api.search(query, selected));
    else symbol.run(() => api.findSymbol(query, selected));
  };

  // Auto-run once on mount if the URL already carries a query (landing here
  // from Home's quick search or a shared link).
  useEffect(() => {
    const initial = params.get("q");
    if (initial?.trim()) runQuery(mode, initial.trim());
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  const onSubmit = (e: FormEvent) => {
    e.preventDefault();
    const query = q.trim();
    if (!query) return;
    setParams({ q: query, mode });
    runQuery(mode, query);
  };

  const switchMode = (m: Mode) => {
    setMode(m);
    if (q.trim()) {
      setParams({ q: q.trim(), mode: m });
      runQuery(m, q.trim());
    }
  };

  return (
    <>
      <PageHeader
        eyebrow="Find"
        title="Search"
        description="Find any function, file, endpoint, or topic by name - across every indexed repository."
      />

      <Card>
        <form onSubmit={onSubmit}>
          <div className="form-row">
            <Segmented
              label="Match"
              value={mode}
              onChange={switchMode}
              options={[
                { value: "fuzzy", label: "Name contains", hint: "Fuzzy substring search" },
                { value: "symbol", label: "Exact name", hint: "Every occurrence of one exact symbol" },
              ]}
            />
          </div>
          <div className="form-row">
            <Input
              label={mode === "fuzzy" ? "Name contains" : "Exact name"}
              value={q}
              onChange={(e) => setQ(e.target.value)}
              placeholder={mode === "fuzzy" ? "e.g. OrderService" : "e.g. ProcessOrder()"}
              autoFocus
            />
          </div>
          <div className="form-row">
            <RepoPicker
              label="Scope"
              value={selected}
              onChange={setSelected}
              hint="Empty = search every indexed repo."
            />
          </div>
          <div className="form-actions">
            <Button type="submit" loading={active.loading} disabled={!q.trim()}>
              Search
            </Button>
          </div>
        </form>
      </Card>

      <div style={{ marginTop: "var(--space-6)" }}>
        <StatusBox
          loading={active.loading}
          error={active.error}
          empty={Array.isArray(active.data) && active.data.length === 0}
          emptyText={
            selected.length > 0
              ? "No matches in the scoped repos - try clearing the repo scope or a shorter fragment."
              : mode === "fuzzy"
                ? "No matches - try a shorter fragment of the name."
                : "No exact match - names are case-insensitive but must match in full. Function names usually end with ()."
          }
        />
        {active.data && <FeedbackWidget endpoint={mode === "fuzzy" ? "search" : "symbol"} query={ratedQuery} />}

        {mode === "fuzzy" && fuzzy.data && fuzzy.data.length > 0 && (
          <DataTable
            rows={fuzzy.data}
            keyFn={(r, i) => r.node_key || String(i)}
            note={fuzzy.data.length === 100 ? "capped at 100 - refine your query to see the rest" : undefined}
            columns={[
              { header: "Name", render: (r) => r.name },
              { header: "Labels", render: (r) => <LabelBadges labels={r.labels} /> },
              { header: "Repo", render: (r) => r.repo },
              { header: "Path", render: (r) => r.path },
              { header: "Line", render: (r) => r.line },
            ]}
          />
        )}

        {mode === "symbol" && symbol.data && symbol.data.length > 0 && (
          <DataTable
            rows={symbol.data}
            keyFn={(r, i) => `${r.repo}:${r.path}:${r.line}:${i}`}
            columns={[
              { header: "Name", render: (r) => r.name },
              { header: "Labels", render: (r) => <LabelBadges labels={r.labels} /> },
              { header: "Repo", render: (r) => r.repo },
              { header: "Path", render: (r) => r.path },
              { header: "Line", render: (r) => r.line },
              { header: "Community", render: (r) => r.community },
            ]}
          />
        )}
      </div>
    </>
  );
}
