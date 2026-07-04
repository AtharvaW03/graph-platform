import { useState, type FormEvent } from "react";
import { api } from "../api";
import { useAsync } from "../hooks/useAsync";
import { StatusBox } from "../components/StatusBox";
import { DataTable, LabelBadges } from "../components/DataTable";
import { FeedbackWidget } from "../components/FeedbackWidget";
import { ScopeBar } from "../components/ScopeBar";
import { useRepoScope } from "../context/RepoScope";
import type { SearchResult } from "../types";

export function SearchPage() {
  const [q, setQ] = useState("");
  const [ratedQuery, setRatedQuery] = useState("");
  const { selected } = useRepoScope();
  const { data, error, loading, run } = useAsync<SearchResult[]>();

  const onSubmit = (e: FormEvent) => {
    e.preventDefault();
    const query = q.trim();
    if (query) {
      setRatedQuery(query);
      run(() => api.search(query, selected));
    }
  };

  return (
    <section>
      <h1>Search Code</h1>
      <p className="hint">
        Partial, case-insensitive match against symbol names.
      </p>
      <ScopeBar />
      <form onSubmit={onSubmit} className="query-form">
        <input
          value={q}
          onChange={(e) => setQ(e.target.value)}
          placeholder="e.g. OrderService"
          aria-label="Search text"
          autoFocus
        />
        <button type="submit" disabled={!q.trim() || loading}>
          Search
        </button>
      </form>
      <StatusBox
        loading={loading}
        error={error}
        empty={data?.length === 0}
        emptyText={
          selected.length > 0
            ? "No matches in the scoped repos - try clearing the repo scope or a shorter fragment."
            : "No matches - try a shorter fragment of the name."
        }
      />
      {data && <FeedbackWidget endpoint="search" query={ratedQuery} />}
      {data && data.length > 0 && (
        <DataTable
          rows={data}
          keyFn={(r, i) => r.node_key || String(i)}
          note={
            data.length === 100
              ? "capped at 100 - refine your query to see the rest"
              : undefined
          }
          columns={[
            { header: "Name", render: (r) => r.name },
            {
              header: "Labels",
              render: (r) => <LabelBadges labels={r.labels} />,
            },
            { header: "Repo", render: (r) => r.repo },
            { header: "Path", render: (r) => r.path },
            { header: "Line", render: (r) => r.line },
          ]}
        />
      )}
    </section>
  );
}
