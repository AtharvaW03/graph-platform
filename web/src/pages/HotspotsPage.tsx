import { useEffect, useState, type FormEvent } from "react";
import { api } from "../api";
import { useAsync } from "../hooks/useAsync";
import { StatusBox } from "../components/StatusBox";
import { DataTable, LabelBadges } from "../components/DataTable";
import { FeedbackWidget } from "../components/FeedbackWidget";
import type { HotspotNode } from "../types";

export function HotspotsPage() {
  const [repo, setRepo] = useState("");
  const [ratedQuery, setRatedQuery] = useState("org-wide");
  const { data, error, loading, run } = useAsync<HotspotNode[]>();

  // Org-wide ranking is the default view, so load it immediately.
  useEffect(() => {
    run(() => api.findHotspots());
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  const onSubmit = (e: FormEvent) => {
    e.preventDefault();
    setRatedQuery(repo.trim() || "org-wide");
    run(() => api.findHotspots(repo.trim() || undefined));
  };

  return (
    <section>
      <h1>Hotspots</h1>
      <p className="hint">
        Code ranked by incoming dependency fan-in: what the most other code
        depends on. High fan-in nodes are high-risk change sites; a dependent
        repos count above 1 means the risk crosses repository boundaries.
      </p>
      <form onSubmit={onSubmit} className="query-form">
        <input
          value={repo}
          onChange={(e) => setRepo(e.target.value)}
          placeholder="repo name (empty = org-wide)"
        />
        <button type="submit">Rank</button>
      </form>
      <StatusBox loading={loading} error={error} empty={data?.length === 0} />
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
