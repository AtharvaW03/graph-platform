import { useState, type FormEvent } from "react";
import { api } from "../api";
import { useAsync } from "../hooks/useAsync";
import { useRepoScope } from "../context/RepoScope";
import { StatusBox } from "../components/StatusBox";
import { DataTable, LabelBadges } from "../components/DataTable";
import { FeedbackWidget } from "../components/FeedbackWidget";
import { RepoPicker } from "../components/RepoPicker";
import { Button, Card, PageHeader } from "../components/ui";
import type { HotspotNode } from "../types";

// No auto-load on open: the org-wide ranking aggregates every dependency
// edge in the graph, so it only runs when explicitly requested. The server
// additionally caches the org-wide result for a few minutes, so repeated
// requests are cheap.
export function Hotspots() {
  const [ratedQuery, setRatedQuery] = useState("");
  const { selected, setSelected } = useRepoScope();
  const { data, error, loading, run } = useAsync<HotspotNode[]>();

  const onSubmit = (e: FormEvent) => {
    e.preventDefault();
    setRatedQuery(selected.length > 0 ? selected.join(",") : "org-wide");
    run(() => api.findHotspots(selected));
  };

  return (
    <>
      <PageHeader
        title="Hotspots"
        description="Code ranked by incoming dependency fan-in: what the most other code depends on. High fan-in nodes are high-risk change sites; a dependent-repos count above 1 means the risk crosses repository boundaries."
      />

      <Card>
        <form onSubmit={onSubmit}>
          <div className="form-row">
            <RepoPicker label="Scope" value={selected} onChange={setSelected} hint="Empty = rank org-wide." />
          </div>
          <div className="form-actions">
            <Button type="submit" loading={loading}>
              {selected.length > 0
                ? `Rank ${selected.length} scoped repo${selected.length > 1 ? "s" : ""}`
                : "Rank org-wide"}
            </Button>
          </div>
        </form>
      </Card>

      <div style={{ marginTop: "var(--space-6)" }}>
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
              { header: "Labels", render: (r) => <LabelBadges labels={r.labels} /> },
              { header: "Repo", render: (r) => r.repo },
              { header: "Fan-in", render: (r) => r.fan_in.toLocaleString() },
              { header: "Dependent Repos", render: (r) => r.dependent_repos },
              { header: "Path", render: (r) => r.path },
              { header: "Line", render: (r) => r.line },
            ]}
          />
        )}
      </div>
    </>
  );
}
