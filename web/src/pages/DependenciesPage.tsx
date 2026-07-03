import { useState, type FormEvent } from "react";
import { api } from "../api";
import { useAsync } from "../hooks/useAsync";
import { StatusBox } from "../components/StatusBox";
import { DataTable, LabelBadges } from "../components/DataTable";
import type { DependencyEdge } from "../types";

type Mode = "dependencies" | "dependents";

export function DependenciesPage() {
  const [mode, setMode] = useState<Mode>("dependencies");
  const [repo, setRepo] = useState("");
  const [scope, setScope] = useState("");
  const [dep, setDep] = useState("");
  const { data, error, loading, run } = useAsync<DependencyEdge[]>();

  const onSubmit = (e: FormEvent) => {
    e.preventDefault();
    if (mode === "dependencies" && repo.trim()) {
      run(() => api.findDependencies(repo.trim(), scope.trim() || undefined));
    } else if (mode === "dependents" && dep.trim()) {
      run(() => api.findDependents(dep.trim()));
    }
  };

  return (
    <section>
      <h1>Dependencies</h1>
      <p className="hint">
        Package/repo dependency edges, including inferred cross-repo
        (DEPENDS_ON_REPO) targets.
      </p>

      <div className="tabs">
        <button
          className={mode === "dependencies" ? "tab active" : "tab"}
          onClick={() => setMode("dependencies")}
        >
          What does a repo depend on?
        </button>
        <button
          className={mode === "dependents" ? "tab active" : "tab"}
          onClick={() => setMode("dependents")}
        >
          Who depends on X?
        </button>
      </div>

      <form onSubmit={onSubmit} className="query-form">
        {mode === "dependencies" ? (
          <>
            <input
              value={repo}
              onChange={(e) => setRepo(e.target.value)}
              placeholder="repo name"
              autoFocus
            />
            <input
              value={scope}
              onChange={(e) => setScope(e.target.value)}
              placeholder="scope filter (optional): runtime, dev, indirect…"
            />
          </>
        ) : (
          <input
            value={dep}
            onChange={(e) => setDep(e.target.value)}
            placeholder="package or repo name"
            autoFocus
          />
        )}
        <button type="submit">Run</button>
      </form>

      <StatusBox loading={loading} error={error} empty={data?.length === 0} />
      {data && data.length > 0 && (
        <DataTable
          rows={data}
          keyFn={(r, i) => `${r.repo}:${r.name}:${i}`}
          columns={[
            {
              header: mode === "dependencies" ? "Depends on" : "Dependent repo",
              render: (r) => r.name,
            },
            {
              header: "Labels",
              render: (r) => <LabelBadges labels={r.labels} />,
            },
            { header: "Ecosystem", render: (r) => r.ecosystem || "—" },
            { header: "Version", render: (r) => r.version || "—" },
            { header: "Scope", render: (r) => r.scope || "—" },
            { header: "Cross-repo", render: (r) => (r.cross_repo ? "✓" : "") },
          ]}
        />
      )}
    </section>
  );
}
