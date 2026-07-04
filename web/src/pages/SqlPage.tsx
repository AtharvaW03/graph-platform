import { useState, type FormEvent } from "react";
import { api } from "../api";
import { useAsync } from "../hooks/useAsync";
import { StatusBox } from "../components/StatusBox";
import { DataTable, LabelBadges, joinList } from "../components/DataTable";
import type { SQLObjectInfo } from "../types";

export function SqlPage() {
  const [schema, setSchema] = useState("");
  const [name, setName] = useState("");
  const { data, error, loading, run } = useAsync<SQLObjectInfo[]>();

  const onSubmit = (e: FormEvent) => {
    e.preventDefault();
    if (name.trim())
      run(() => api.findSQLObject(schema.trim() || undefined, name.trim()));
  };

  return (
    <section>
      <h1>SQL Object</h1>
      <p className="hint">
        Schemas, tables, views, procedures, triggers, functions - plus what each
        reads, writes, and depends on. Leave schema blank to match across
        schemas.
      </p>
      <form onSubmit={onSubmit} className="query-form">
        <input
          value={schema}
          onChange={(e) => setSchema(e.target.value)}
          placeholder="schema (optional)"
        />
        <input
          value={name}
          onChange={(e) => setName(e.target.value)}
          placeholder="object name"
          autoFocus
        />
        <button type="submit">Look up</button>
      </form>
      <StatusBox loading={loading} error={error} empty={data?.length === 0} />
      {data && data.length > 0 && (
        <DataTable
          rows={data}
          keyFn={(r, i) => `${r.schema}.${r.name}:${i}`}
          columns={[
            { header: "Schema", render: (r) => r.schema },
            { header: "Name", render: (r) => r.name },
            { header: "Kind", render: (r) => r.kind },
            {
              header: "Labels",
              render: (r) => <LabelBadges labels={r.labels} />,
            },
            { header: "Reads", render: (r) => joinList(r.reads) },
            { header: "Writes", render: (r) => joinList(r.writes) },
            { header: "Depends on", render: (r) => joinList(r.depends_on) },
            { header: "Triggers on", render: (r) => r.triggers_on || "-" },
            { header: "File", render: (r) => r.file || "-" },
          ]}
        />
      )}
    </section>
  );
}
