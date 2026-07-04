import { useRepoScope } from "../context/RepoScope";

// ScopeBar renders above every page: a dropdown of indexed repositories
// (checkbox multi-select, no names to remember) plus chips for the current
// selection. Empty selection = all repos. Scoping applies to symbol-level
// pages (search, symbols, paths, routes, hotspots); inherently single-repo
// or org-global pages say so themselves.
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
