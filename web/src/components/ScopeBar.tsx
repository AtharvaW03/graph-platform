import { useEffect, useRef, useState } from "react";
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
  const dropdownRef = useRef<HTMLDetailsElement>(null);
  const searchRef = useRef<HTMLInputElement>(null);
  const [filter, setFilter] = useState("");

  // <details> only closes via its own summary; a dropdown must also close
  // on outside click and Escape. The menu itself stays open across checkbox
  // clicks (multi-select needs that).
  useEffect(() => {
    const closeOnOutsideClick = (e: MouseEvent) => {
      const d = dropdownRef.current;
      if (d?.open && e.target instanceof Node && !d.contains(e.target)) {
        d.open = false;
      }
    };
    const closeOnEscape = (e: KeyboardEvent) => {
      if (e.key === "Escape" && dropdownRef.current) {
        dropdownRef.current.open = false;
      }
    };
    document.addEventListener("mousedown", closeOnOutsideClick);
    document.addEventListener("keydown", closeOnEscape);
    return () => {
      document.removeEventListener("mousedown", closeOnOutsideClick);
      document.removeEventListener("keydown", closeOnEscape);
    };
  }, []);

  const toggle = (name: string) =>
    setSelected(
      selected.includes(name)
        ? selected.filter((x) => x !== name)
        : [...selected, name],
    );

  const q = filter.trim().toLowerCase();
  const shown = q
    ? available.filter((r) => r.name.toLowerCase().includes(q))
    : available;

  return (
    <div className="scope-bar">
      <span className="scope-label">Repo scope:</span>
      <details
        className="scope-dropdown"
        ref={dropdownRef}
        onToggle={() => {
          if (dropdownRef.current?.open) {
            setFilter("");
            queueMicrotask(() => searchRef.current?.focus());
          }
        }}
      >
        <summary>
          {selected.length === 0
            ? "All repos"
            : `${selected.length} selected`}{" "}
          ▾
        </summary>
        <div className="scope-menu">
          {available.length > 0 && (
            <input
              ref={searchRef}
              className="repo-search"
              value={filter}
              onChange={(e) => setFilter(e.target.value)}
              placeholder="search repos…"
              aria-label="Search repositories"
            />
          )}
          {available.length === 0 && (
            <div className="dim small">no repositories indexed yet</div>
          )}
          {available.length > 0 && shown.length === 0 && (
            <div className="dim small">no matches</div>
          )}
          {shown.map((r) => (
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
