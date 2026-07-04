import { useState, type FormEvent } from "react";
import { api } from "../api";
import { useAsync } from "../hooks/useAsync";
import { StatusBox } from "../components/StatusBox";
import { DataTable, LabelBadges } from "../components/DataTable";
import type { HTTPRoute } from "../types";

export function RoutesPage() {
  const [method, setMethod] = useState("");
  const [path, setPath] = useState("");
  const [repo, setRepo] = useState("");
  const { data, error, loading, run } = useAsync<HTTPRoute[]>();

  const onSubmit = (e: FormEvent) => {
    e.preventDefault();
    run(() =>
      api.findRoutes(
        method.trim() || undefined,
        path.trim() || undefined,
        repo.trim() || undefined,
      ),
    );
  };

  return (
    <section>
      <h1>HTTP Routes</h1>
      <p className="hint">
        Search the HTTP API inventory across all repositories. Leave fields
        blank to match any.
      </p>
      <form onSubmit={onSubmit} className="query-form">
        <input
          value={method}
          onChange={(e) => setMethod(e.target.value)}
          placeholder="method (GET, POST…)"
        />
        <input
          value={path}
          onChange={(e) => setPath(e.target.value)}
          placeholder="path substring"
          autoFocus
        />
        <input
          value={repo}
          onChange={(e) => setRepo(e.target.value)}
          placeholder="repo (optional)"
        />
        <button type="submit">Search</button>
      </form>
      <StatusBox loading={loading} error={error} empty={data?.length === 0} />
      {data && data.length > 0 && (
        <DataTable
          rows={data}
          keyFn={(r, i) => `${r.repo}:${r.method}:${r.path}:${i}`}
          columns={[
            { header: "Method", render: (r) => r.method },
            { header: "Path", render: (r) => r.path },
            { header: "Repo", render: (r) => r.repo },
            { header: "Handler", render: (r) => r.handler || "-" },
            {
              header: "Labels",
              render: (r) => <LabelBadges labels={r.labels} />,
            },
            { header: "File", render: (r) => r.file },
            { header: "Line", render: (r) => r.line },
          ]}
        />
      )}
    </section>
  );
}
