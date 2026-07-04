import { useState, type FormEvent } from "react";
import { api } from "../api";
import { useAsync } from "../hooks/useAsync";
import { StatusBox } from "../components/StatusBox";
import { DataTable, LabelBadges } from "../components/DataTable";
import { FeedbackWidget } from "../components/FeedbackWidget";
import { ScopeBar } from "../components/ScopeBar";
import { useRepoScope } from "../context/RepoScope";
import type { HTTPRoute } from "../types";

export function RoutesPage() {
  const [method, setMethod] = useState("");
  const [path, setPath] = useState("");
  const [ratedQuery, setRatedQuery] = useState("");
  const { selected } = useRepoScope();
  const { data, error, loading, run } = useAsync<HTTPRoute[]>();

  const onSubmit = (e: FormEvent) => {
    e.preventDefault();
    setRatedQuery(
      [method.trim(), path.trim()].filter(Boolean).join(" ") || "all routes",
    );
    run(() =>
      api.findRoutes(
        method.trim() || undefined,
        path.trim() || undefined,
        selected,
      ),
    );
  };

  return (
    <section>
      <h1>HTTP Routes</h1>
      <p className="hint">
        Search the HTTP API inventory. Leave fields blank to match any.
      </p>
      <ScopeBar />
      <form onSubmit={onSubmit} className="query-form">
        <input
          value={method}
          onChange={(e) => setMethod(e.target.value)}
          placeholder="method (GET, POST…)"
          aria-label="HTTP method filter"
        />
        <input
          value={path}
          onChange={(e) => setPath(e.target.value)}
          placeholder="path substring"
          aria-label="Path substring filter"
          autoFocus
        />
        <button type="submit" disabled={loading}>
          Search
        </button>
      </form>
      <StatusBox
        loading={loading}
        error={error}
        empty={data?.length === 0}
        emptyText={
          selected.length > 0
            ? "No routes matched in the scoped repos - try clearing the repo scope or the filters."
            : "No routes matched - try clearing a filter."
        }
      />
      {data && <FeedbackWidget endpoint="routes" query={ratedQuery} />}
      {data && data.length > 0 && (
        <DataTable
          rows={data}
          keyFn={(r, i) => `${r.repo}:${r.method}:${r.path}:${i}`}
          note={
            data.length === 500
              ? "capped at 500 - add a method/path filter to see the rest"
              : undefined
          }
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
