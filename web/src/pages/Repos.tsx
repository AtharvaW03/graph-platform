import { useState } from "react";
import { api } from "../api";
import { useAsync } from "../hooks/useAsync";
import { useRepoScope } from "../context/RepoScope";
import { StatusBox } from "../components/StatusBox";
import { joinList } from "../components/DataTable";
import { FeedbackWidget } from "../components/FeedbackWidget";
import { Badge, Button, Card, PageHeader, Stat } from "../components/ui";
import type { RepositoryOverview } from "../types";

// Repos folds two things together: the plain indexed-repo list, and (once
// one is selected) the same repository-overview detail that used to be its
// own top-level page - onboarding content ("architecture, entry points,
// modules, APIs, dependencies") naturally lives under "pick a repo, see
// what's there".
export function Repos() {
  const { available, loading: reposLoading, error: reposError, refresh } = useRepoScope();
  const [selected, setSelected] = useState("");
  const overview = useAsync<RepositoryOverview>();

  const onSelect = (name: string) => {
    setSelected(name);
    overview.run(() => api.repositoryOverview(name));
  };

  return (
    <>
      <PageHeader
        title="Repos"
        description="Every indexed repository. Select one for its architecture, entry points, modules, and dependency summary."
        actions={
          <Button variant="secondary" size="sm" onClick={() => refresh()} loading={reposLoading}>
            Refresh
          </Button>
        }
      />

      <div className="home-grid">
        <Card as="section">
          <h3 style={{ marginBottom: "var(--space-3)" }}>Indexed repositories</h3>
          <StatusBox
            loading={reposLoading}
            error={reposError}
            empty={available.length === 0}
            emptyText="No repositories indexed yet - run the indexer, then refresh."
          />
          {available.length > 0 && (
            <ul className="repo-list">
              {available.map((r) => (
                <li key={r.name}>
                  <button
                    type="button"
                    className={`repo-list-item ${r.name === selected ? "is-active" : ""}`}
                    onClick={() => onSelect(r.name)}
                  >
                    <span className="mono">{r.name}</span>
                    <span className="dim">{r.nodes.toLocaleString()} nodes</span>
                  </button>
                </li>
              ))}
            </ul>
          )}
        </Card>

        <Card as="section">
          <h3 style={{ marginBottom: "var(--space-3)" }}>
            {selected || "Overview"}
          </h3>
          {!selected && (
            <p className="dim">Select a repository from the list to see its overview.</p>
          )}
          {selected && (
            <>
              <StatusBox loading={overview.loading} error={overview.error} />
              {overview.data && <FeedbackWidget endpoint="overview" query={selected} />}
              {overview.data && <OverviewBody ov={overview.data} />}
            </>
          )}
        </Card>
      </div>
    </>
  );
}

function OverviewBody({ ov }: { ov: RepositoryOverview }) {
  return (
    <div>
      <p style={{ marginBottom: "var(--space-4)" }}>{ov.architecture.summary}</p>

      <div className="grid-stats">
        <Stat label="Nodes" value={ov.repository.node_count.toLocaleString()} />
        <Stat label="Relationships" value={ov.repository.relationship_count.toLocaleString()} />
        <Stat label="Communities" value={ov.architecture.communities.length} />
        <Stat label="HTTP Routes" value={ov.http_apis.route_count} />
        <Stat label="Kafka Topics" value={ov.kafka.topics.length} />
      </div>

      <h2 style={{ marginTop: "var(--space-6)" }}>Languages</h2>
      <div className="badge-row">
        {ov.repository.languages.map((l) => (
          <Badge key={l.name}>{l.name} × {l.count}</Badge>
        ))}
      </div>
      <h2>Node Kinds</h2>
      <div className="badge-row">
        {ov.repository.node_kinds.map((k) => (
          <Badge key={k.name}>{k.name} × {k.count}</Badge>
        ))}
      </div>

      {ov.entry_points.length > 0 && (
        <>
          <h2>Entry Points</h2>
          <ul>
            {ov.entry_points.map((ep, i) => (
              <li key={i}>
                <strong>{ep.name}</strong> <span className="dim">({ep.kind})</span> - {ep.path}:{ep.line}
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
                <div className="small">{joinList(c.sample_members)}</div>
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
                <code>{m.package}</code> - {m.node_count} nodes, {m.functions} functions
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
                <code>{g.prefix}</code> - {g.count} routes ({joinList(g.methods)})
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

      {(ov.sql.tables.length > 0 || ov.sql.procedures.length > 0 || ov.sql.views.length > 0) && (
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

      {(ov.dependencies.internal_repos.length > 0 || ov.dependencies.external.length > 0) && (
        <>
          <h2>Dependencies</h2>
          {ov.dependencies.internal_repos.length > 0 && (
            <p>
              <strong>Internal (cross-repo):</strong> {joinList(ov.dependencies.internal_repos)}
            </p>
          )}
          <p>
            <strong>Top ecosystems:</strong>{" "}
            {ov.dependencies.top_ecosystems.map((e) => `${e.name} (${e.count})`).join(", ") || "-"}
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
                <strong>{step.category}</strong> - <span className="dim">{step.why}</span>
                <div className="small">{joinList(step.items)}</div>
              </li>
            ))}
          </ol>
        </>
      )}
    </div>
  );
}
