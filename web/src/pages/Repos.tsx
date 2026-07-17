import { useMemo, useState, type ReactNode } from "react";
import { api } from "../api";
import { useAsync } from "../hooks/useAsync";
import { useRepoScope } from "../context/RepoScope";
import { StatusBox } from "../components/StatusBox";
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
  const [filter, setFilter] = useState("");
  const overview = useAsync<RepositoryOverview>();

  const filtered = useMemo(() => {
    const q = filter.trim().toLowerCase();
    if (!q) return available;
    return available.filter((r) => r.name.toLowerCase().includes(q));
  }, [available, filter]);

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
          {available.length > 0 && (
            <div className="repo-search">
              <input
                type="search"
                className="field__input"
                placeholder="Filter repositories…"
                aria-label="Filter repositories by name"
                value={filter}
                onChange={(e) => setFilter(e.target.value)}
              />
              <span className="small" aria-live="polite">
                {filtered.length === available.length
                  ? `${available.length} repositories`
                  : `${filtered.length} of ${available.length}`}
              </span>
            </div>
          )}
          <StatusBox
            loading={reposLoading}
            error={reposError}
            empty={available.length === 0}
            emptyText="No repositories indexed yet - run the indexer, then refresh."
          />
          {available.length > 0 && filtered.length === 0 && (
            <p className="dim">No repositories match "{filter}".</p>
          )}
          {filtered.length > 0 && (
            <ul className="repo-list repo-list--scroll">
              {filtered.map((r) => (
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

// Section renders one collapsible block of the overview: a title with a
// count, verbose detail hidden behind a native <details> so the page reads
// as a scannable outline instead of a wall of lists. Sections a newcomer
// acts on first (entry points, APIs) start open; inventories start closed.
function Section({
  title,
  count,
  defaultOpen = false,
  children,
}: {
  title: string;
  count?: number | string;
  defaultOpen?: boolean;
  children: ReactNode;
}) {
  return (
    <details className="ov-section" open={defaultOpen}>
      <summary>
        <span className="ov-section__title">{title}</span>
        {count !== undefined && <span className="ov-section__count">{count}</span>}
      </summary>
      <div className="ov-section__body">{children}</div>
    </details>
  );
}

// capped joins the first n items and summarizes the rest, so a repo with
// 200 SQL tables doesn't paint 200 names onto the page.
function capped(items: string[], n: number): ReactNode {
  if (items.length === 0) return <span className="dim">-</span>;
  const shown = items.slice(0, n);
  const rest = items.length - shown.length;
  return (
    <>
      {shown.join(", ")}
      {rest > 0 && <span className="dim"> +{rest} more</span>}
    </>
  );
}

function OverviewBody({ ov }: { ov: RepositoryOverview }) {
  const sql = ov.sql;
  const sqlCounts = [
    ["Schemas", sql.schemas.length],
    ["Tables", sql.tables.length],
    ["Views", sql.views.length],
    ["Procedures", sql.procedures.length],
    ["Functions", sql.functions.length],
    ["Triggers", sql.triggers.length],
  ].filter(([, n]) => (n as number) > 0) as [string, number][];
  const sqlTotal = sqlCounts.reduce((acc, [, n]) => acc + n, 0);
  const kafkaTotal = ov.kafka.topics.length + ov.kafka.producers.length + ov.kafka.consumers.length;

  return (
    <div className="ov">
      <p className="ov-summary">{ov.architecture.summary}</p>

      <div className="grid-stats">
        <Stat label="Nodes" value={ov.repository.node_count.toLocaleString()} />
        <Stat label="Relationships" value={ov.repository.relationship_count.toLocaleString()} />
        <Stat label="HTTP Routes" value={ov.http_apis.route_count} />
        <Stat label="Kafka Topics" value={ov.kafka.topics.length} />
        <Stat label="Modules" value={ov.modules.length} />
      </div>

      {ov.repository.languages.length > 0 && (
        <div className="badge-row" aria-label="Languages">
          {ov.repository.languages.map((l) => (
            <Badge key={l.name} tone="brand">
              {l.name} × {l.count}
            </Badge>
          ))}
        </div>
      )}

      {ov.entry_points.length > 0 && (
        <Section title="Entry points" count={ov.entry_points.length} defaultOpen>
          {ov.entry_points.map((ep, i) => (
            <div className="ov-row" key={i}>
              <span>
                <strong>{ep.name}</strong> <Badge>{ep.kind}</Badge>
              </span>
              <span className="mono small">
                {ep.path}:{ep.line}
              </span>
            </div>
          ))}
        </Section>
      )}

      {ov.http_apis.groups.length > 0 && (
        <Section title="HTTP APIs" count={ov.http_apis.route_count} defaultOpen>
          {ov.http_apis.methods.length > 0 && (
            <div className="badge-row">
              {ov.http_apis.methods.map((m) => (
                <Badge key={m.name} tone="info">
                  {m.name} × {m.count}
                </Badge>
              ))}
            </div>
          )}
          {ov.http_apis.groups.map((g) => (
            <div className="ov-row" key={g.prefix}>
              <code>{g.prefix}</code>
              <span className="small">
                {g.count} routes · {g.methods.join(" ")}
              </span>
            </div>
          ))}
        </Section>
      )}

      {ov.architecture.communities.length > 0 && (
        <Section title="Architecture communities" count={ov.architecture.communities.length}>
          {ov.architecture.communities.map((c) => (
            <div className="ov-row ov-row--stacked" key={c.id}>
              <span>
                <strong>{c.label}</strong>
                <span className="dim">
                  {" "}
                  · {c.size} nodes{c.dominant_dir ? ` · ${c.dominant_dir}` : ""}
                </span>
              </span>
              <span className="small">{capped(c.sample_members, 3)}</span>
            </div>
          ))}
        </Section>
      )}

      {ov.modules.length > 0 && (
        <Section title="Modules" count={ov.modules.length}>
          {ov.modules.map((m) => (
            <div className="ov-row" key={m.package}>
              <code>{m.package}</code>
              <span className="small">
                {m.node_count} nodes · {m.functions} functions
              </span>
            </div>
          ))}
        </Section>
      )}

      {kafkaTotal > 0 && (
        <Section title="Kafka" count={ov.kafka.topics.length}>
          <div className="ov-kv">
            <span className="ov-kv__key">Topics</span>
            <span>{capped(ov.kafka.topics, 8)}</span>
            <span className="ov-kv__key">Producers</span>
            <span>{capped(ov.kafka.producers, 6)}</span>
            <span className="ov-kv__key">Consumers</span>
            <span>{capped(ov.kafka.consumers, 6)}</span>
          </div>
        </Section>
      )}

      {sqlTotal > 0 && (
        <Section title="SQL objects" count={sqlTotal}>
          <div className="badge-row">
            {sqlCounts.map(([name, n]) => (
              <Badge key={name}>
                {name} × {n}
              </Badge>
            ))}
          </div>
          <div className="ov-kv">
            {sql.tables.length > 0 && (
              <>
                <span className="ov-kv__key">Tables</span>
                <span>{capped(sql.tables, 8)}</span>
              </>
            )}
            {sql.procedures.length > 0 && (
              <>
                <span className="ov-kv__key">Procedures</span>
                <span>{capped(sql.procedures, 8)}</span>
              </>
            )}
            {sql.views.length > 0 && (
              <>
                <span className="ov-kv__key">Views</span>
                <span>{capped(sql.views, 8)}</span>
              </>
            )}
          </div>
        </Section>
      )}

      {(ov.dependencies.internal_repos.length > 0 || ov.dependencies.top_ecosystems.length > 0) && (
        <Section
          title="Dependencies"
          count={ov.dependencies.internal_repos.length + ov.dependencies.top_ecosystems.length}
        >
          {ov.dependencies.internal_repos.length > 0 && (
            <>
              <p className="ov-kv__key">Internal (cross-repo)</p>
              <div className="badge-row">
                {ov.dependencies.internal_repos.map((r) => (
                  <Badge key={r} tone="brand">
                    {r}
                  </Badge>
                ))}
              </div>
            </>
          )}
          {ov.dependencies.top_ecosystems.length > 0 && (
            <>
              <p className="ov-kv__key">Top ecosystems</p>
              <div className="badge-row">
                {ov.dependencies.top_ecosystems.map((e) => (
                  <Badge key={e.name}>
                    {e.name} × {e.count}
                  </Badge>
                ))}
              </div>
            </>
          )}
        </Section>
      )}

      {ov.important_components.length > 0 && (
        <Section title="Hub components" count={ov.important_components.length}>
          <p className="small" style={{ marginBottom: "var(--space-2)" }}>
            Highest-degree nodes - the code most other code touches.
          </p>
          {ov.important_components.map((c, i) => (
            <div className="ov-row" key={i}>
              <strong>{c.name}</strong>
              <span className="small">
                degree {c.degree} · <span className="mono">{c.path}</span>
              </span>
            </div>
          ))}
        </Section>
      )}

      {ov.suggested_reading_order.length > 0 && (
        <Section title="Suggested reading order" count={ov.suggested_reading_order.length} defaultOpen>
          <ol className="ov-reading">
            {ov.suggested_reading_order.map((step, i) => (
              <li key={i}>
                <strong>{step.category}</strong>
                <span className="dim"> - {step.why}</span>
                <div className="small">{capped(step.items, 5)}</div>
              </li>
            ))}
          </ol>
        </Section>
      )}
    </div>
  );
}
