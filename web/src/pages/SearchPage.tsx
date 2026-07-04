import { useState, type FormEvent } from "react";
import { api } from "../api";
import { useAsync } from "../hooks/useAsync";
import { StatusBox } from "../components/StatusBox";
import { DataTable, LabelBadges } from "../components/DataTable";
import { FeedbackWidget } from "../components/FeedbackWidget";
import type { SearchResult } from "../types";

export function SearchPage() {
  const [q, setQ] = useState("");
  const [ratedQuery, setRatedQuery] = useState("");
  const { data, error, loading, run } = useAsync<SearchResult[]>();

  const onSubmit = (e: FormEvent) => {
    e.preventDefault();
    const query = q.trim();
    if (query) {
      setRatedQuery(query);
      run(() => api.search(query));
    }
  };

  return (
    <section>
      <h1>Search Code</h1>
      <p className="hint">
        Partial, case-insensitive match against symbol name across all imported
        repositories.
      </p>
      <form onSubmit={onSubmit} className="query-form">
        <input
          value={q}
          onChange={(e) => setQ(e.target.value)}
          placeholder="e.g. OrderService"
          autoFocus
        />
        <button type="submit">Search</button>
      </form>
      <StatusBox loading={loading} error={error} empty={data?.length === 0} />
      {data && <FeedbackWidget endpoint="search" query={ratedQuery} />}
      {data && data.length > 0 && (
        <DataTable
          rows={data}
          keyFn={(r, i) => r.node_key || String(i)}
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
