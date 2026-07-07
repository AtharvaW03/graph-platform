import { useEffect, useRef, useState } from "react";
import type { RepoInfo } from "../types";

// RepoSelect is a searchable single-repo picker: a <details> dropdown with a
// filter box, for pages that operate on ONE repo (overview, dependencies).
// It replaces a native <select>, which forces a scroll through every repo -
// unworkable once an org has dozens or hundreds indexed. The multi-select
// scope picker (ScopeBar) has its own in-menu search of the same shape.
//
// Close-on-outside-click and Escape mirror ScopeBar: a <details> only closes
// via its summary otherwise.
interface Props {
  available: RepoInfo[];
  value: string;
  onSelect: (name: string) => void;
  placeholder?: string;
  autoFocusOnOpen?: boolean;
}

export function RepoSelect({
  available,
  value,
  onSelect,
  placeholder = "select a repository…",
  autoFocusOnOpen = true,
}: Props) {
  const ref = useRef<HTMLDetailsElement>(null);
  const searchRef = useRef<HTMLInputElement>(null);
  const [filter, setFilter] = useState("");

  useEffect(() => {
    const closeOnOutsideClick = (e: MouseEvent) => {
      const d = ref.current;
      if (d?.open && e.target instanceof Node && !d.contains(e.target)) {
        d.open = false;
      }
    };
    const closeOnEscape = (e: KeyboardEvent) => {
      if (e.key === "Escape" && ref.current) ref.current.open = false;
    };
    document.addEventListener("mousedown", closeOnOutsideClick);
    document.addEventListener("keydown", closeOnEscape);
    return () => {
      document.removeEventListener("mousedown", closeOnOutsideClick);
      document.removeEventListener("keydown", closeOnEscape);
    };
  }, []);

  const q = filter.trim().toLowerCase();
  const shown = q
    ? available.filter((r) => r.name.toLowerCase().includes(q))
    : available;

  const choose = (name: string) => {
    onSelect(name);
    setFilter("");
    if (ref.current) ref.current.open = false;
  };

  return (
    <details
      className="scope-dropdown repo-select"
      ref={ref}
      onToggle={() => {
        if (ref.current?.open) {
          setFilter("");
          if (autoFocusOnOpen) queueMicrotask(() => searchRef.current?.focus());
        }
      }}
    >
      <summary>
        {value || <span className="dim">{placeholder}</span>} ▾
      </summary>
      <div className="scope-menu">
        <input
          ref={searchRef}
          className="repo-search"
          value={filter}
          onChange={(e) => setFilter(e.target.value)}
          placeholder="search repos…"
          aria-label="Search repositories"
          onKeyDown={(e) => {
            if (e.key === "Enter" && shown.length > 0) {
              e.preventDefault();
              choose(shown[0].name);
            }
          }}
        />
        {available.length === 0 && (
          <div className="dim small">no repositories indexed yet</div>
        )}
        {available.length > 0 && shown.length === 0 && (
          <div className="dim small">no matches</div>
        )}
        {shown.map((r) => (
          <button
            type="button"
            key={r.name}
            className={
              "scope-option" + (r.name === value ? " selected" : "")
            }
            onClick={() => choose(r.name)}
          >
            {r.name}{" "}
            <span className="dim">({r.nodes.toLocaleString()} nodes)</span>
          </button>
        ))}
      </div>
    </details>
  );
}
