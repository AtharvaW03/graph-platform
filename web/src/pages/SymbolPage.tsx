import { useState, type FormEvent } from "react";
import { api } from "../api";
import { useAsync } from "../hooks/useAsync";
import { StatusBox } from "../components/StatusBox";
import { DataTable, LabelBadges } from "../components/DataTable";
import { FeedbackWidget } from "../components/FeedbackWidget";
import { ScopeBar } from "../components/ScopeBar";
import { useRepoScope } from "../context/RepoScope";
import type { CallEdge, ImpactNode, SymbolResult } from "../types";

type Mode = "occurrences" | "callers" | "callees" | "blast-radius";

const modes: { id: Mode; label: string }[] = [
  { id: "occurrences", label: "Occurrences" },
  { id: "callers", label: "Callers" },
  { id: "callees", label: "Callees" },
  { id: "blast-radius", label: "Blast Radius" },
];

export function SymbolPage() {
  const [symbol, setSymbol] = useState("");
  const [depth, setDepth] = useState(3);
  const [mode, setMode] = useState<Mode>("occurrences");
  const [ratedQuery, setRatedQuery] = useState("");
  const { selected } = useRepoScope();

  const occ = useAsync<SymbolResult[]>();
  const callers = useAsync<CallEdge[]>();
  const callees = useAsync<CallEdge[]>();
  const blast = useAsync<ImpactNode[]>();

  const runFor = (m: Mode, sym: string) => {
    setRatedQuery(`${m}: ${sym}`);
    switch (m) {
      case "occurrences":
        return occ.run(() => api.findSymbol(sym, selected));
      case "callers":
        return callers.run(() => api.findCallers(sym, selected));
      case "callees":
        return callees.run(() => api.findCallees(sym, selected));
      case "blast-radius":
        return blast.run(() => api.blastRadius(sym, depth, selected));
    }
  };

  const onSubmit = (e: FormEvent) => {
    e.preventDefault();
    if (symbol.trim()) runFor(mode, symbol.trim());
  };

  const switchMode = (m: Mode) => {
    setMode(m);
    if (symbol.trim()) runFor(m, symbol.trim());
  };

  const active = { occurrences: occ, callers, callees, "blast-radius": blast }[
    mode
  ];

  return (
    <section>
      <h1>Symbol Explorer</h1>
      <p className="hint">
        Exact symbol name. Combines find_symbol, find_callers, find_callees, and
        blast_radius.
      </p>
      <ScopeBar />
      <form onSubmit={onSubmit} className="query-form">
        <input
          value={symbol}
          onChange={(e) => setSymbol(e.target.value)}
          placeholder="e.g. ProcessOrder()"
          aria-label="Symbol name"
          autoFocus
        />
        {mode === "blast-radius" && (
          <input
            type="number"
            min={1}
            max={10}
            value={depth}
            onChange={(e) => setDepth(Number(e.target.value))}
            className="depth-input"
            aria-label="Traversal depth"
            title="traversal depth"
          />
        )}
        <button type="submit" disabled={!symbol.trim() || active.loading}>
          Run
        </button>
      </form>

      <div className="tabs">
        {modes.map((m) => (
          <button
            key={m.id}
            className={m.id === mode ? "tab active" : "tab"}
            onClick={() => switchMode(m.id)}
          >
            {m.label}
          </button>
        ))}
      </div>

      <StatusBox
        loading={active.loading}
        error={active.error}
        empty={Array.isArray(active.data) && active.data.length === 0}
        emptyText={
          selected.length > 0
            ? "No results in the scoped repos - try clearing the repo scope. Function names usually end with (), e.g. ProcessOrder()."
            : "No results - exact match only. Function names usually end with (), e.g. ProcessOrder()."
        }
      />
      {active.data && <FeedbackWidget endpoint="symbol" query={ratedQuery} />}

      {mode === "occurrences" && occ.data && occ.data.length > 0 && (
        <DataTable
          rows={occ.data}
          keyFn={(r, i) => `${r.repo}:${r.path}:${r.line}:${i}`}
          columns={[
            { header: "Name", render: (r) => r.name },
            {
              header: "Labels",
              render: (r) => <LabelBadges labels={r.labels} />,
            },
            { header: "Repo", render: (r) => r.repo },
            { header: "Path", render: (r) => r.path },
            { header: "Line", render: (r) => r.line },
            { header: "Community", render: (r) => r.community },
          ]}
        />
      )}

      {mode === "callers" && callers.data && callers.data.length > 0 && (
        <DataTable
          rows={callers.data}
          keyFn={(r, i) =>
            `${r.caller_repo}:${r.caller_path}:${r.caller_line}:${i}`
          }
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
            {
              header: "Labels",
              render: (r) => <LabelBadges labels={r.labels} />,
            },
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
            {
              header: "Labels",
              render: (r) => <LabelBadges labels={r.labels} />,
            },
            { header: "Repo", render: (r) => r.repo },
            { header: "Path", render: (r) => r.path },
          ]}
        />
      )}
    </section>
  );
}
