import { useState, type FormEvent } from "react";
import { api } from "../api";
import { useAsync } from "../hooks/useAsync";
import { useRepoScope } from "../context/RepoScope";
import { StatusBox } from "../components/StatusBox";
import { DataTable, LabelBadges } from "../components/DataTable";
import { FeedbackWidget } from "../components/FeedbackWidget";
import { RepoPicker } from "../components/RepoPicker";
import { Button, Card, Input, PageHeader, Select } from "../components/ui";
import type { CallEdge, ImpactNode, PathNode } from "../types";

type Mode = "callers" | "callees" | "blast-radius" | "path";

const MODES: { id: Mode; label: string }[] = [
  { id: "blast-radius", label: "Blast radius" },
  { id: "callers", label: "Callers" },
  { id: "callees", label: "Callees" },
  { id: "path", label: "Shortest path" },
];

export function Impact() {
  const [mode, setMode] = useState<Mode>("blast-radius");
  const [symbol, setSymbol] = useState("");
  const [target, setTarget] = useState("");
  const [depth, setDepth] = useState(3);
  const [ratedQuery, setRatedQuery] = useState("");
  const { selected, setSelected } = useRepoScope();

  const callers = useAsync<CallEdge[]>();
  const callees = useAsync<CallEdge[]>();
  const blast = useAsync<ImpactNode[]>();
  const path = useAsync<PathNode[]>();

  const active = { callers, callees, "blast-radius": blast, path }[mode];

  const onSubmit = (e: FormEvent) => {
    e.preventDefault();
    if (mode === "path") {
      if (!symbol.trim() || !target.trim()) return;
      setRatedQuery(`${symbol.trim()} -> ${target.trim()}`);
      path.run(() => api.shortestPath(symbol.trim(), target.trim(), selected));
      return;
    }
    if (!symbol.trim()) return;
    setRatedQuery(`${mode}: ${symbol.trim()}`);
    if (mode === "callers") callers.run(() => api.findCallers(symbol.trim(), selected));
    else if (mode === "callees") callees.run(() => api.findCallees(symbol.trim(), selected));
    else blast.run(() => api.blastRadius(symbol.trim(), depth, selected));
  };

  const canSubmit = mode === "path" ? symbol.trim() && target.trim() : symbol.trim().length > 0;

  return (
    <>
      <PageHeader
        title="Impact"
        description="Trace how a symbol connects to the rest of the graph - what calls it, what it calls, its blast radius, or the shortest path to another symbol."
      />

      <Card>
        <form onSubmit={onSubmit}>
          <div className="form-row form-row--2">
            <Select label="Mode" value={mode} onChange={(e) => setMode(e.target.value as Mode)}>
              {MODES.map((m) => (
                <option key={m.id} value={m.id}>{m.label}</option>
              ))}
            </Select>
            <Input
              label={mode === "path" ? "Source symbol" : "Symbol"}
              value={symbol}
              onChange={(e) => setSymbol(e.target.value)}
              placeholder="e.g. ProcessOrder()"
              autoFocus
            />
          </div>
          {mode === "path" && (
            <div className="form-row">
              <Input
                label="Target symbol"
                value={target}
                onChange={(e) => setTarget(e.target.value)}
                placeholder="e.g. SendReceipt()"
              />
            </div>
          )}
          {mode === "blast-radius" && (
            <div className="form-row">
              <Input
                label="Traversal depth"
                type="number"
                min={1}
                max={10}
                value={depth}
                onChange={(e) => setDepth(Number(e.target.value))}
              />
            </div>
          )}
          <div className="form-row">
            <RepoPicker label="Scope" value={selected} onChange={setSelected} hint="Empty = every indexed repo." />
          </div>
          <div className="form-actions">
            <Button type="submit" loading={active.loading} disabled={!canSubmit}>
              Run
            </Button>
          </div>
        </form>
      </Card>

      <div style={{ marginTop: "var(--space-6)" }}>
        <StatusBox
          loading={active.loading}
          error={active.error}
          empty={Array.isArray(active.data) && active.data.length === 0}
          emptyText={
            mode === "path"
              ? "No path found within 15 hops - check both symbol names exist, or clear the repo scope."
              : selected.length > 0
                ? "No results in the scoped repos - try clearing the repo scope. Function names usually end with ()."
                : "No results - exact match only. Function names usually end with ()."
          }
        />
        {active.data && <FeedbackWidget endpoint={mode === "path" ? "path" : "impact"} query={ratedQuery} />}

        {mode === "callers" && callers.data && callers.data.length > 0 && (
          <DataTable
            rows={callers.data}
            keyFn={(r, i) => `${r.caller_repo}:${r.caller_path}:${r.caller_line}:${i}`}
            columns={[
              { header: "Caller", render: (r) => r.caller },
              { header: "Repo", render: (r) => r.caller_repo },
              { header: "Path", render: (r) => r.caller_path },
              { header: "Line", render: (r) => r.caller_line },
              { header: "Callee", render: (r) => r.callee },
            ]}
          />
        )}

        {mode === "callees" && callees.data && callees.data.length > 0 && (
          <DataTable
            rows={callees.data}
            keyFn={(r, i) => `${r.callee_repo}:${r.callee_path}:${i}`}
            columns={[
              { header: "Caller", render: (r) => r.caller },
              { header: "Callee", render: (r) => r.callee },
              { header: "Labels", render: (r) => <LabelBadges labels={r.labels} /> },
              { header: "Repo", render: (r) => r.callee_repo },
              { header: "Path", render: (r) => r.callee_path },
            ]}
          />
        )}

        {mode === "blast-radius" && blast.data && blast.data.length > 0 && (
          <DataTable
            rows={blast.data}
            keyFn={(r, i) => `${r.repo}:${r.path}:${i}`}
            columns={[
              { header: "Distance", render: (r) => r.distance },
              { header: "Name", render: (r) => r.name },
              { header: "Labels", render: (r) => <LabelBadges labels={r.labels} /> },
              { header: "Repo", render: (r) => r.repo },
              { header: "Path", render: (r) => r.path },
            ]}
          />
        )}

        {mode === "path" && path.data && path.data.length > 0 && (
          <ol className="path-trail">
            {path.data.map((node, i) => (
              <li key={`${node.repo}:${node.path}:${i}`}>
                {node.relationship && (
                  <span className="rel-arrow">--[{node.relationship}]--&gt;</span>
                )}
                <strong>{node.name}</strong>{" "}
                <span className="mono">
                  ({node.labels.filter((l) => l !== "Entity").join(", ")}) - {node.repo} - {node.path}
                </span>
              </li>
            ))}
          </ol>
        )}
      </div>
    </>
  );
}
