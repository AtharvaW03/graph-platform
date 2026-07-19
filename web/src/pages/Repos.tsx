import { useEffect, useMemo, useState, type ReactNode } from "react";
import { useSearchParams } from "react-router-dom";
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
  const [params] = useSearchParams();
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

  // A ?repo=name link opens that service directly once the list has loaded.
  const linked = params.get("repo");
  useEffect(() => {
    if (linked && !selected && available.some((r) => r.name === linked)) {
      onSelect(linked);
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [available, linked]);

  return (
    <>
      <PageHeader
        eyebrow="Understand"
        title="Services"
        description="Every indexed repository. Open one for a guided overview: what it is, its APIs, and how it connects to the rest of the org."
        actions={
          <Button variant="secondary" size="sm" onClick={() => refresh()} loading={reposLoading}>
            Refresh
          </Button>
        }
      />

      <div className="repos-grid">
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
                  ? `${available.length}`
                  : `${filtered.length} / ${available.length}`}
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
                    <span className="dim">{r.nodes.toLocaleString()}</span>
                  </button>
                </li>
              ))}
            </ul>
          )}
        </Card>

        <Card as="section" className="repos-detail">
          <h3 style={{ marginBottom: "var(--space-3)" }}>{selected || "Overview"}</h3>
          {!selected && (
            <p className="dim">Select a repository from the list to see its overview.</p>
          )}
          {selected && (
            <>
              <StatusBox loading={overview.loading} error={overview.error} />
              {overview.data && <OverviewBody ov={overview.data} />}
              {overview.data && <FeedbackWidget endpoint="overview" query={selected} />}
            </>
          )}
        </Card>
      </div>
    </>
  );
}

// Section renders one collapsible overview block: a summary row with title
// and count, detail inside a native <details>.
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

// Chips renders an identifier list as mono tokens, capped with a "+N more"
// marker.
function Chips({ items, cap = 8 }: { items: string[]; cap?: number }) {
  if (items.length === 0) return <span className="dim">-</span>;
  const shown = items.slice(0, cap);
  const rest = items.length - shown.length;
  return (
    <div className="chip-group">
      {shown.map((it) => (
        <span className="chip" key={it}>
          {it}
        </span>
      ))}
      {rest > 0 && <span className="chip chip--more">+{rest} more</span>}
    </div>
  );
}

function Group({ label, children }: { label: string; children: ReactNode }) {
  return (
    <div className="ov-group">
      <p className="ov-group__label">{label}</p>
      {children}
    </div>
  );
}

// Tbl is a static table on the shared .table styles; the per-section data is
// small and pre-sorted by the API.
function Tbl({ head, rows }: { head: string[]; rows: ReactNode[][] }) {
  return (
    <div className="table-wrap">
      <table className="table">
        <thead>
          <tr>
            {head.map((h) => (
              <th key={h}>{h}</th>
            ))}
          </tr>
        </thead>
        <tbody>
          {rows.map((cells, i) => (
            <tr key={i}>
              {cells.map((c, j) => (
                <td key={j}>{c}</td>
              ))}
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

function OverviewBody({ ov }: { ov: RepositoryOverview }) {
  const sql = ov.sql;
  const sqlGroups = [
    ["Tables", sql.tables],
    ["Views", sql.views],
    ["Procedures", sql.procedures],
    ["Functions", sql.functions],
    ["Triggers", sql.triggers],
  ].filter(([, items]) => (items as string[]).length > 0) as [string, string[]][];
  const sqlTotal = sqlGroups.reduce((acc, [, items]) => acc + items.length, 0);
  const kafkaTotal = ov.kafka.topics.length + ov.kafka.producers.length + ov.kafka.consumers.length;

  return (
    <div className="ov">
      <p className="ov-summary">{ov.architecture.summary}</p>

      <div className="grid-stats">
        <Stat label="Code elements" value={ov.repository.node_count.toLocaleString()} />
        <Stat label="Connections" value={ov.repository.relationship_count.toLocaleString()} />
        <Stat label="HTTP endpoints" value={ov.http_apis.route_count} />
        <Stat label="Kafka topics" value={ov.kafka.topics.length} />
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
          <Tbl
            head={["Name", "Kind", "Location"]}
            rows={ov.entry_points.map((ep) => [
              <strong key="n">{ep.name}</strong>,
              <Badge key="k">{ep.kind}</Badge>,
              <span key="l" className="mono small">
                {ep.path}:{ep.line}
              </span>,
            ])}
          />
        </Section>
      )}

      {ov.http_apis.groups.length > 0 && (
        <Section title="HTTP APIs" count={ov.http_apis.route_count} defaultOpen>
          {ov.http_apis.methods.length > 0 && (
            <Group label="Methods">
              <div className="badge-row">
                {ov.http_apis.methods.map((m) => (
                  <Badge key={m.name} tone="info">
                    {m.name} × {m.count}
                  </Badge>
                ))}
              </div>
            </Group>
          )}
          <Tbl
            head={["Path prefix", "Routes", "Methods"]}
            rows={ov.http_apis.groups.map((g) => [
              <code key="p">{g.prefix}</code>,
              g.count,
              <span key="m" className="small">{g.methods.join(" · ")}</span>,
            ])}
          />
        </Section>
      )}

      {ov.architecture.communities.length > 0 && (
        <Section title="Architecture communities" count={ov.architecture.communities.length}>
          <Tbl
            head={["Community", "Size", "Mainly in", "Examples"]}
            rows={ov.architecture.communities.map((c) => [
              <strong key="n">{c.label}</strong>,
              c.size,
              c.dominant_dir ? <span key="d" className="mono small">{c.dominant_dir}</span> : "-",
              <Chips key="s" items={c.sample_members} cap={3} />,
            ])}
          />
        </Section>
      )}

      {ov.modules.length > 0 && (
        <Section title="Modules" count={ov.modules.length}>
          <Tbl
            head={["Package", "Nodes", "Functions"]}
            rows={ov.modules.map((m) => [<code key="p">{m.package}</code>, m.node_count, m.functions])}
          />
        </Section>
      )}

      {kafkaTotal > 0 && (
        <Section title="Kafka" count={ov.kafka.topics.length}>
          <Group label="Topics">
            <Chips items={ov.kafka.topics} />
          </Group>
          <Group label="Producers">
            <Chips items={ov.kafka.producers} cap={6} />
          </Group>
          <Group label="Consumers">
            <Chips items={ov.kafka.consumers} cap={6} />
          </Group>
        </Section>
      )}

      {sqlTotal > 0 && (
        <Section title="SQL objects" count={sqlTotal}>
          {sql.schemas.length > 0 && (
            <Group label="Schemas">
              <Chips items={sql.schemas} cap={6} />
            </Group>
          )}
          {sqlGroups.map(([label, items]) => (
            <Group key={label} label={`${label} (${items.length})`}>
              <Chips items={items} />
            </Group>
          ))}
        </Section>
      )}

      {(ov.dependencies.internal_repos.length > 0 || ov.dependencies.top_ecosystems.length > 0) && (
        <Section
          title="Dependencies"
          count={ov.dependencies.internal_repos.length + ov.dependencies.top_ecosystems.length}
        >
          {ov.dependencies.internal_repos.length > 0 && (
            <Group label="Depends on (in this graph)">
              <div className="badge-row">
                {ov.dependencies.internal_repos.map((r) => (
                  <Badge key={r} tone="brand">
                    {r}
                  </Badge>
                ))}
              </div>
            </Group>
          )}
          {ov.dependencies.top_ecosystems.length > 0 && (
            <Group label="External ecosystems">
              <div className="badge-row">
                {ov.dependencies.top_ecosystems.map((e) => (
                  <Badge key={e.name}>
                    {e.name} × {e.count}
                  </Badge>
                ))}
              </div>
            </Group>
          )}
        </Section>
      )}

      {ov.important_components.length > 0 && (
        <Section title="Hub components" count={ov.important_components.length}>
          <p className="small" style={{ marginBottom: "var(--space-3)" }}>
            The code most other code touches - changes here have the widest blast radius.
          </p>
          <Tbl
            head={["Component", "Connections", "File"]}
            rows={ov.important_components.map((c) => [
              <strong key="n">{c.name}</strong>,
              c.degree,
              <span key="f" className="mono small">{c.path}</span>,
            ])}
          />
        </Section>
      )}

      {ov.suggested_reading_order.length > 0 && (
        <Section
          title="Suggested reading order"
          count={ov.suggested_reading_order.length}
          defaultOpen
        >
          <ol className="ov-reading">
            {ov.suggested_reading_order.map((step, i) => (
              <li key={i}>
                <strong>{step.category}</strong>
                <div className="small">{step.why}</div>
                <Chips items={step.items} cap={5} />
              </li>
            ))}
          </ol>
        </Section>
      )}
    </div>
  );
}
