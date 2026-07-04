import { useState, type FormEvent } from "react";
import { api } from "../api";
import { useAsync } from "../hooks/useAsync";
import { StatusBox } from "../components/StatusBox";
import { DataTable, LabelBadges, joinList } from "../components/DataTable";
import type { GlueJobInfo } from "../types";

export function GluePage() {
  const [source, setSource] = useState("");
  const [target, setTarget] = useState("");
  const { data, error, loading, run } = useAsync<GlueJobInfo[]>();

  const onSubmit = (e: FormEvent) => {
    e.preventDefault();
    run(() =>
      api.findGlueJobs(source.trim() || undefined, target.trim() || undefined),
    );
  };

  return (
    <section>
      <h1>Glue Jobs</h1>
      <p className="hint">
        Search AWS Glue jobs by source or destination table. Leave both blank to
        list every job.
      </p>
      <form onSubmit={onSubmit} className="query-form">
        <input
          value={source}
          onChange={(e) => setSource(e.target.value)}
          placeholder="source table (schema.table)"
        />
        <input
          value={target}
          onChange={(e) => setTarget(e.target.value)}
          placeholder="destination table"
        />
        <button type="submit">Search</button>
      </form>
      <StatusBox loading={loading} error={error} empty={data?.length === 0} />
      {data && data.length > 0 && (
        <DataTable
          rows={data}
          keyFn={(r, i) => `${r.repo}:${r.name}:${i}`}
          columns={[
            { header: "Job", render: (r) => r.name },
            { header: "Repo", render: (r) => r.repo },
            {
              header: "Labels",
              render: (r) => <LabelBadges labels={r.labels} />,
            },
            { header: "Schedule", render: (r) => r.schedule || "-" },
            { header: "Sources", render: (r) => joinList(r.sources) },
            { header: "Targets", render: (r) => joinList(r.targets) },
            { header: "Script", render: (r) => r.script || "-" },
          ]}
        />
      )}
    </section>
  );
}
