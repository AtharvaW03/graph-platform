import { useState, type FormEvent } from "react";
import { api } from "../api";
import { useAsync } from "../hooks/useAsync";
import { StatusBox } from "../components/StatusBox";
import { DataTable, LabelBadges } from "../components/DataTable";
import { FeedbackWidget } from "../components/FeedbackWidget";
import { ScopeBar } from "../components/ScopeBar";
import { useRepoScope } from "../context/RepoScope";
import type { HotspotNode } from "../types";

// No auto-load on open: the org-wide ranking aggregates every dependency
// edge in the graph, so it only runs when explicitly requested. The server
// additionally caches the org-wide result for a few minutes, so repeated
// requests are cheap.
export function HotspotsPage() {
  const [ratedQuery, setRatedQuery] = useState("");
  const { selected } = useRepoScope();
  const { data, error, loading, run } = useAsync<HotspotNode[]>();

  const onSubmit = (e: FormEvent) => {
    e.preventDefault();
    setRatedQuery(selected.length > 0 ? selected.join(",") : "org-wide");
    run(() => api.findHotspots(selected));
  };

  return (
    <section>
      <h1>Hotspots</h1>
      <p className="hint">
        Code ranked by incoming dependency fan-in: what the most other code
        depends on. High fan-in nodes are high-risk change sites; a dependent
        repos count above 1 means the risk crosses repository boundaries.
      </p>
      <ScopeBar />
      <form onSubmit={onSubmit} className="query-form">
        <button type="submit" disabled={loading}>
          {selected.length > 0
            ? `Rank ${selected.length} scoped repo${selected.length > 1 ? "s" : ""}`
            : "Rank org-wide"}
        </button>
      </form>
      <StatusBox
        loading={loading}
        error={error}
        empty={data?.length === 0}
        emptyText="No dependency edges indexed yet - run the indexer first."
      />
      {data && <FeedbackWidget endpoint="hotspots" query={ratedQuery} />}
      {data && data.length > 0 && (
        <DataTable
          rows={data}
          keyFn={(r, i) => `${r.repo}:${r.name}:${i}`}
          columns={[
            { header: "Name", render: (r) => r.name },
            {
              header: "Labels",
              render: (r) => <LabelBadges labels={r.labels} />,
            },
            { header: "Repo", render: (r) => r.repo },
            { header: "Fan-in", render: (r) => r.fan_in.toLocaleString() },
            { header: "Dependent Repos", render: (r) => r.dependent_repos },
            { header: "Path", render: (r) => r.path },
            { header: "Line", render: (r) => r.line },
          ]}
        />
      )}
    </section>
  );
}
