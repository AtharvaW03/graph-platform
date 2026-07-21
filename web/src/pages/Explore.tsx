import { useState, type FormEvent } from "react";
import { api } from "../api";
import { useAsync } from "../hooks/useAsync";
import { useRepoScope } from "../context/RepoScope";
import { StatusBox } from "../components/StatusBox";
import { DataTable, LabelBadges, ConfidenceBadge, joinList } from "../components/DataTable";
import { FeedbackWidget } from "../components/FeedbackWidget";
import { RepoPicker } from "../components/RepoPicker";
import { Button, Card, Input, PageHeader, Segmented } from "../components/ui";
import type {
  DependencyEdge,
  GlueJobInfo,
  HTTPRoute,
  KafkaTopicInfo,
  SQLObjectInfo,
} from "../types";

type Kind = "routes" | "kafka" | "sql" | "glue" | "dependencies";

const KINDS: { value: Kind; label: string }[] = [
  { value: "routes", label: "HTTP endpoints" },
  { value: "kafka", label: "Kafka topics" },
  { value: "sql", label: "SQL objects" },
  { value: "glue", label: "Glue jobs" },
  { value: "dependencies", label: "Dependencies" },
];

export function Explore() {
  const [kind, setKind] = useState<Kind>("routes");

  return (
    <>
      <PageHeader
        eyebrow="Find"
        title="Catalog"
        description="Browse everything the org runs on: HTTP endpoints, Kafka topics, SQL objects, Glue jobs, and dependencies."
      />

      <Card>
        <div className="form-row">
          <Segmented label="What to browse" value={kind} onChange={setKind} options={KINDS} />
        </div>
        {kind === "routes" && <RoutesForm />}
        {kind === "kafka" && <KafkaForm />}
        {kind === "sql" && <SqlForm />}
        {kind === "glue" && <GlueForm />}
        {kind === "dependencies" && <DependenciesForm />}
      </Card>
    </>
  );
}

function RoutesForm() {
  const [method, setMethod] = useState("");
  const [path, setPath] = useState("");
  const [ratedQuery, setRatedQuery] = useState("");
  const { selected, setSelected } = useRepoScope();
  const { data, error, loading, run } = useAsync<HTTPRoute[]>();

  const onSubmit = (e: FormEvent) => {
    e.preventDefault();
    setRatedQuery([method.trim(), path.trim()].filter(Boolean).join(" ") || "all routes");
    run(() => api.findRoutes(method.trim() || undefined, path.trim() || undefined, selected));
  };

  return (
    <>
      <form onSubmit={onSubmit}>
        <div className="form-row form-row--2">
          <Input label="Method" value={method} onChange={(e) => setMethod(e.target.value)} placeholder="GET, POST…" />
          <Input label="Path substring" value={path} onChange={(e) => setPath(e.target.value)} placeholder="/orders" />
        </div>
        <div className="form-row">
          <RepoPicker label="Scope" value={selected} onChange={setSelected} hint="Empty = every indexed repo." />
        </div>
        <div className="form-actions">
          <Button type="submit" loading={loading}>Search</Button>
        </div>
      </form>
      <div style={{ marginTop: "var(--space-6)" }}>
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
            note={data.length === 500 ? "capped at 500 - add a method/path filter to see the rest" : undefined}
            columns={[
              { header: "Method", render: (r) => r.method },
              { header: "Path", render: (r) => r.path },
              { header: "Repo", render: (r) => r.repo },
              { header: "Handler", render: (r) => r.handler || "-" },
              { header: "Labels", render: (r) => <LabelBadges labels={r.labels} /> },
              { header: "File", render: (r) => r.file },
              { header: "Line", render: (r) => r.line },
            ]}
          />
        )}
      </div>
    </>
  );
}

function KafkaForm() {
  const [topic, setTopic] = useState("");
  const [ratedQuery, setRatedQuery] = useState("");
  const { data, error, loading, run } = useAsync<KafkaTopicInfo>();

  const onSubmit = (e: FormEvent) => {
    e.preventDefault();
    if (!topic.trim()) return;
    setRatedQuery(topic.trim());
    run(() => api.findKafkaTopic(topic.trim()));
  };

  return (
    <>
      <form onSubmit={onSubmit}>
        <div className="form-row">
          <Input
            label="Topic name"
            value={topic}
            onChange={(e) => setTopic(e.target.value)}
            placeholder="e.g. order.created"
            hint="Exact topic name lookup - returns the repos that produce to and consume from it."
            autoFocus
          />
        </div>
        <div className="form-actions">
          <Button type="submit" loading={loading} disabled={!topic.trim()}>Look up</Button>
        </div>
      </form>
      <div style={{ marginTop: "var(--space-6)" }}>
        <StatusBox loading={loading} error={error} />
        {data && <FeedbackWidget endpoint="kafka" query={ratedQuery} />}
        {data && (
          <dl className="kv">
            <dt>Topic</dt>
            <dd>{data.topic}</dd>
            <dt>Producers</dt>
            <dd>{joinList(data.producers)}</dd>
            <dt>Consumers</dt>
            <dd>{joinList(data.consumers)}</dd>
          </dl>
        )}
      </div>
    </>
  );
}

function SqlForm() {
  const [schema, setSchema] = useState("");
  const [name, setName] = useState("");
  const [ratedQuery, setRatedQuery] = useState("");
  const { data, error, loading, run } = useAsync<SQLObjectInfo[]>();

  const onSubmit = (e: FormEvent) => {
    e.preventDefault();
    if (!name.trim()) return;
    setRatedQuery(schema.trim() ? `${schema.trim()}.${name.trim()}` : name.trim());
    run(() => api.findSQLObject(schema.trim() || undefined, name.trim()));
  };

  return (
    <>
      <form onSubmit={onSubmit}>
        <div className="form-row form-row--2">
          <Input label="Schema (optional)" value={schema} onChange={(e) => setSchema(e.target.value)} placeholder="dbo" />
          <Input
            label="Object name"
            value={name}
            onChange={(e) => setName(e.target.value)}
            placeholder="Orders"
            hint="Schemas, tables, views, procedures, triggers, functions - plus what each reads, writes, and depends on."
            autoFocus
          />
        </div>
        <div className="form-actions">
          <Button type="submit" loading={loading} disabled={!name.trim()}>Look up</Button>
        </div>
      </form>
      <div style={{ marginTop: "var(--space-6)" }}>
        <StatusBox loading={loading} error={error} empty={data?.length === 0} />
        {data && <FeedbackWidget endpoint="sql" query={ratedQuery} />}
        {data && data.length > 0 && (
          <DataTable
            rows={data}
            keyFn={(r, i) => `${r.schema}.${r.name}:${i}`}
            columns={[
              { header: "Schema", render: (r) => r.schema },
              { header: "Name", render: (r) => r.name },
              { header: "Kind", render: (r) => r.kind },
              { header: "Labels", render: (r) => <LabelBadges labels={r.labels} /> },
              { header: "Reads", render: (r) => joinList(r.reads) },
              { header: "Writes", render: (r) => joinList(r.writes) },
              { header: "Depends on", render: (r) => joinList(r.depends_on) },
              { header: "Triggers on", render: (r) => r.triggers_on || "-" },
              { header: "File", render: (r) => r.file || "-" },
            ]}
          />
        )}
      </div>
    </>
  );
}

function GlueForm() {
  const [source, setSource] = useState("");
  const [target, setTarget] = useState("");
  const [ratedQuery, setRatedQuery] = useState("");
  const { data, error, loading, run } = useAsync<GlueJobInfo[]>();

  const onSubmit = (e: FormEvent) => {
    e.preventDefault();
    setRatedQuery([source.trim(), target.trim()].filter(Boolean).join(" -> ") || "all glue jobs");
    run(() => api.findGlueJobs(source.trim() || undefined, target.trim() || undefined));
  };

  return (
    <>
      <form onSubmit={onSubmit}>
        <div className="form-row form-row--2">
          <Input label="Source table" value={source} onChange={(e) => setSource(e.target.value)} placeholder="schema.table" />
          <Input label="Destination table" value={target} onChange={(e) => setTarget(e.target.value)} placeholder="schema.table" />
        </div>
        <p className="field__hint" style={{ marginBottom: "var(--space-3)" }}>Leave both blank to list every job.</p>
        <div className="form-actions">
          <Button type="submit" loading={loading}>Search</Button>
        </div>
      </form>
      <div style={{ marginTop: "var(--space-6)" }}>
        <StatusBox loading={loading} error={error} empty={data?.length === 0} />
        {data && <FeedbackWidget endpoint="glue" query={ratedQuery} />}
        {data && data.length > 0 && (
          <DataTable
            rows={data}
            keyFn={(r, i) => `${r.repo}:${r.name}:${i}`}
            columns={[
              { header: "Job", render: (r) => r.name },
              { header: "Repo", render: (r) => r.repo },
              { header: "Labels", render: (r) => <LabelBadges labels={r.labels} /> },
              { header: "Schedule", render: (r) => r.schedule || "-" },
              { header: "Sources", render: (r) => joinList(r.sources) },
              { header: "Targets", render: (r) => joinList(r.targets) },
              { header: "Script", render: (r) => r.script || "-" },
            ]}
          />
        )}
      </div>
    </>
  );
}

type DepDirection = "dependencies" | "dependents";

function DependenciesForm() {
  const [direction, setDirection] = useState<DepDirection>("dependencies");
  const [repo, setRepo] = useState<string[]>([]);
  const [scope, setScope] = useState("");
  const [dep, setDep] = useState("");
  const [ratedQuery, setRatedQuery] = useState("");
  const { data, error, loading, run } = useAsync<DependencyEdge[]>();

  const onSubmit = (e: FormEvent) => {
    e.preventDefault();
    if (direction === "dependencies" && repo[0]) {
      setRatedQuery(`dependencies of ${repo[0]}`);
      run(() => api.findDependencies(repo[0], scope.trim() || undefined));
    } else if (direction === "dependents" && dep.trim()) {
      setRatedQuery(`dependents of ${dep.trim()}`);
      run(() => api.findDependents(dep.trim()));
    }
  };

  return (
    <>
      <div className="tabs">
        <button
          type="button"
          className={direction === "dependencies" ? "tab active" : "tab"}
          onClick={() => setDirection("dependencies")}
        >
          What does a repo depend on?
        </button>
        <button
          type="button"
          className={direction === "dependents" ? "tab active" : "tab"}
          onClick={() => setDirection("dependents")}
        >
          Who depends on X?
        </button>
      </div>

      <form onSubmit={onSubmit}>
        {direction === "dependencies" ? (
          <>
            <div className="form-row">
              <RepoPicker label="Repository" value={repo} onChange={setRepo} multiple={false} required />
            </div>
            <div className="form-row">
              <Input
                label="Scope filter (optional)"
                value={scope}
                onChange={(e) => setScope(e.target.value)}
                placeholder="runtime, dev, indirect…"
              />
            </div>
          </>
        ) : (
          <div className="form-row">
            <Input
              label="Package or repository name"
              value={dep}
              onChange={(e) => setDep(e.target.value)}
              placeholder="e.g. github.com/example-org/auth-service"
              autoFocus
            />
          </div>
        )}
        <div className="form-actions">
          <Button
            type="submit"
            loading={loading}
            disabled={direction === "dependencies" ? repo.length === 0 : !dep.trim()}
          >
            Run
          </Button>
        </div>
      </form>

      <div style={{ marginTop: "var(--space-6)" }}>
        <StatusBox loading={loading} error={error} empty={data?.length === 0} />
        {data && <FeedbackWidget endpoint="dependencies" query={ratedQuery} />}
        {data && data.length > 0 && (
          <DataTable
            rows={data}
            keyFn={(r, i) => `${r.repo}:${r.name}:${i}`}
            columns={[
              { header: direction === "dependencies" ? "Depends on" : "Dependent repo", render: (r) => r.name },
              { header: "Labels", render: (r) => <LabelBadges labels={r.labels} /> },
              { header: "Ecosystem", render: (r) => r.ecosystem || "-" },
              { header: "Version", render: (r) => r.version || "-" },
              { header: "Scope", render: (r) => r.scope || "-" },
              { header: "Cross-repo", render: (r) => (r.cross_repo ? "✓" : "") },
              { header: "Confidence", render: (r) => <ConfidenceBadge confidence={r.confidence} /> },
            ]}
          />
        )}
      </div>
    </>
  );
}
