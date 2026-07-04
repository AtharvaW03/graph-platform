import { useRepoScope } from "../context/RepoScope";

// ScopeBar is rendered ONLY by pages whose query is meaningfully scopable
// (search, symbol explorer, shortest path, HTTP routes, hotspots - all
// queries over repo-owned entities across repos). Pages that pick their own
// single repo (overview, dependencies) or query org-global entities (kafka,
// sql, glue) must not render it - a control that does nothing on the
// current page is worse than no control.
//
// The selection itself is shared and persisted (context + localStorage), so
// a scope chosen on Search still applies when the user lands on Hotspots.
export function ScopeBar() {
  const { available, selected, setSelected } = useRepoScope();

  const toggle = (name: string) =>
    setSelected(
      selected.includes(name)
        ? selected.filter((x) => x !== name)
        : [...selected, name],
    );

  return (
    <div className="scope-bar">
      <span className="scope-label">Repo scope:</span>
      <details className="scope-dropdown">
        <summary>
          {selected.length === 0
            ? "All repos"
            : `${selected.length} selected`}{" "}
          ▾
        </summary>
        <div className="scope-menu">
          {available.length === 0 && (
            <div className="dim small">no repositories indexed yet</div>
          )}
          {available.map((r) => (
            <label key={r.name} className="scope-option">
              <input
                type="checkbox"
                checked={selected.includes(r.name)}
                onChange={() => toggle(r.name)}
              />{" "}
              {r.name}{" "}
              <span className="dim">({r.nodes.toLocaleString()} nodes)</span>
            </label>
          ))}
        </div>
      </details>
      {selected.map((name) => (
        <button
          key={name}
          type="button"
          className="chip chip-muted scope-chip"
          onClick={() => toggle(name)}
          title="remove from scope"
        >
          {name} ✕
        </button>
      ))}
      {selected.length > 0 && (
        <button
          type="button"
          className="scope-clear"
          onClick={() => setSelected([])}
        >
          clear
        </button>
      )}
    </div>
  );
}
