import { useState, type FormEvent } from "react";
import { api } from "../api";
import { useAsync } from "../hooks/useAsync";
import { StatusBox } from "../components/StatusBox";
import { joinList } from "../components/DataTable";
import type { RepositoryOverview } from "../types";

export function OverviewPage() {
  const [repo, setRepo] = useState("");
  const { data, error, loading, run } = useAsync<RepositoryOverview>();

  const onSubmit = (e: FormEvent) => {
    e.preventDefault();
    if (repo.trim()) run(() => api.repositoryOverview(repo.trim()));
  };

  return (
    <section>
      <h1>Repository Overview</h1>
      <p className="hint">
        Primary onboarding entry point - architecture, entry points, modules,
        APIs, dependencies.
      </p>
      <form onSubmit={onSubmit} className="query-form">
        <input
          value={repo}
          onChange={(e) => setRepo(e.target.value)}
          placeholder="repo name"
          autoFocus
        />
        <button type="submit">Load</button>
      </form>
      <StatusBox loading={loading} error={error} />
      {data && <OverviewBody ov={data} />}
    </section>
  );
}

function OverviewBody({ ov }: { ov: RepositoryOverview }) {
  return (
    <div className="overview">
      <p className="overview-summary">{ov.architecture.summary}</p>

      <div className="stat-row">
        <Stat label="Nodes" value={ov.repository.node_count} />
        <Stat label="Relationships" value={ov.repository.relationship_count} />
        <Stat label="Communities" value={ov.architecture.communities.length} />
        <Stat label="HTTP Routes" value={ov.http_apis.route_count} />
        <Stat label="Kafka Topics" value={ov.kafka.topics.length} />
      </div>

      {/* Informational only, not clickable filters: keep every chip the same
          muted style so none of them reads as "selected". */}
      <h2>Languages</h2>
      <div className="chip-row">
        {ov.repository.languages.map((l) => (
          <span key={l.name} className="chip chip-muted">
            {l.name} × {l.count}
          </span>
        ))}
      </div>
      <h2>Node Kinds</h2>
      <div className="chip-row">
        {ov.repository.node_kinds.map((k) => (
          <span key={k.name} className="chip chip-muted">
            {k.name} × {k.count}
          </span>
        ))}
      </div>

      {ov.entry_points.length > 0 && (
        <>
          <h2>Entry Points</h2>
          <ul>
            {ov.entry_points.map((ep, i) => (
              <li key={i}>
                <strong>{ep.name}</strong>{" "}
                <span className="dim">({ep.kind})</span> - {ep.path}:{ep.line}
              </li>
            ))}
          </ul>
        </>
      )}

      {ov.architecture.communities.length > 0 && (
        <>
          <h2>Communities</h2>
          <ul>
            {ov.architecture.communities.map((c) => (
              <li key={c.id}>
                <strong>{c.label}</strong> - {c.size} nodes
                {c.dominant_dir ? ` · ${c.dominant_dir}` : ""}
                <div className="dim small">{joinList(c.sample_members)}</div>
              </li>
            ))}
          </ul>
        </>
      )}

      {ov.modules.length > 0 && (
        <>
          <h2>Modules</h2>
          <ul>
            {ov.modules.map((m) => (
              <li key={m.package}>
                <code>{m.package}</code> - {m.node_count} nodes, {m.functions}{" "}
                functions
              </li>
            ))}
          </ul>
        </>
      )}

      {ov.http_apis.groups.length > 0 && (
        <>
          <h2>HTTP API Groups</h2>
          <ul>
            {ov.http_apis.groups.map((g) => (
              <li key={g.prefix}>
                <code>{g.prefix}</code> - {g.count} routes (
                {joinList(g.methods)})
              </li>
            ))}
          </ul>
        </>
      )}

      {ov.kafka.topics.length > 0 && (
        <>
          <h2>Kafka</h2>
          <p>
            Topics: {joinList(ov.kafka.topics)}
            <br />
            Producers: {joinList(ov.kafka.producers)}
            <br />
            Consumers: {joinList(ov.kafka.consumers)}
          </p>
        </>
      )}

      {(ov.sql.tables.length > 0 ||
        ov.sql.procedures.length > 0 ||
        ov.sql.views.length > 0) && (
        <>
          <h2>SQL</h2>
          <p>
            Schemas: {joinList(ov.sql.schemas)}
            <br />
            Tables: {joinList(ov.sql.tables)}
            <br />
            Views: {joinList(ov.sql.views)}
            <br />
            Procedures: {joinList(ov.sql.procedures)}
            <br />
            Functions: {joinList(ov.sql.functions)}
            <br />
            Triggers: {joinList(ov.sql.triggers)}
          </p>
        </>
      )}

      {(ov.dependencies.internal_repos.length > 0 ||
        ov.dependencies.external.length > 0) && (
        <>
          <h2>Dependencies</h2>
          {ov.dependencies.internal_repos.length > 0 && (
            <p>
              <strong>Internal (cross-repo):</strong>{" "}
              {joinList(ov.dependencies.internal_repos)}
            </p>
          )}
          <p>
            <strong>Top ecosystems:</strong>{" "}
            {ov.dependencies.top_ecosystems
              .map((e) => `${e.name} (${e.count})`)
              .join(", ") || "-"}
          </p>
        </>
      )}

      {ov.important_components.length > 0 && (
        <>
          <h2>Hub Components (highest-degree nodes)</h2>
          <ul>
            {ov.important_components.map((c, i) => (
              <li key={i}>
                <strong>{c.name}</strong> - degree {c.degree} - {c.path}
              </li>
            ))}
          </ul>
        </>
      )}

      {ov.suggested_reading_order.length > 0 && (
        <>
          <h2>Suggested Reading Order</h2>
          <ol>
            {ov.suggested_reading_order.map((step, i) => (
              <li key={i}>
                <strong>{step.category}</strong> -{" "}
                <span className="dim">{step.why}</span>
                <div className="small">{joinList(step.items)}</div>
              </li>
            ))}
          </ol>
        </>
      )}
    </div>
  );
}

function Stat({ label, value }: { label: string; value: number }) {
  return (
    <div className="stat">
      <div className="stat-value">{value.toLocaleString()}</div>
      <div className="stat-label">{label}</div>
    </div>
  );
}
