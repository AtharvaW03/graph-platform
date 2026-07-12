import {
  useCallback,
  useEffect,
  useId,
  useMemo,
  useRef,
  useState,
  type KeyboardEvent,
} from "react";
import { useRepoScope } from "../context/RepoScope";
import type { RepoInfo } from "../types";
import "./RepoPicker.css";

type Props = {
  label?: string;
  hint?: string;
  /** empty = all repos (no filter) */
  value: string[];
  onChange: (repos: string[]) => void;
  /** multi-select (default true). false = pick at most one */
  multiple?: boolean;
  /** required: cannot clear to empty */
  required?: boolean;
  disabled?: boolean;
  className?: string;
};

// Searchable repo dropdown for large catalogs (100+): type to filter,
// checkbox multi-select, chips for selected. The repo list itself comes from
// the shared RepoScope context (GET /repos, fetched once), not a per-instance
// fetch - so this component and the caller always agree on what's indexed,
// and multiple pickers on the same page (or across pages, for the scope use)
// never race each other's loads.
export function RepoPicker({
  label = "Repositories",
  hint,
  value,
  onChange,
  multiple = true,
  required = false,
  disabled = false,
  className = "",
}: Props) {
  const { available: repos, loading, error, refresh } = useRepoScope();
  const [open, setOpen] = useState(false);
  const [filter, setFilter] = useState("");
  const [highlight, setHighlight] = useState(0);
  const rootRef = useRef<HTMLDivElement>(null);
  const searchRef = useRef<HTMLInputElement>(null);
  const listId = useId();
  const labelId = useId();

  const filtered = useMemo(() => {
    const q = filter.trim().toLowerCase();
    if (!q) return repos;
    return repos.filter(
      (r) =>
        r.name.toLowerCase().includes(q) ||
        r.name.toLowerCase().split("/").some((p) => p.includes(q)),
    );
  }, [repos, filter]);

  useEffect(() => {
    if (highlight >= filtered.length) setHighlight(0);
  }, [filtered.length, highlight]);

  useEffect(() => {
    if (!open) return;
    const t = window.setTimeout(() => searchRef.current?.focus(), 0);
    return () => clearTimeout(t);
  }, [open]);

  useEffect(() => {
    function onDoc(e: MouseEvent) {
      if (!rootRef.current?.contains(e.target as Node)) {
        setOpen(false);
        setFilter("");
      }
    }
    document.addEventListener("mousedown", onDoc);
    return () => document.removeEventListener("mousedown", onDoc);
  }, []);

  const selectedSet = useMemo(() => new Set(value), [value]);

  const toggle = useCallback(
    (name: string) => {
      if (multiple) {
        if (selectedSet.has(name)) {
          if (required && value.length <= 1) return;
          onChange(value.filter((v) => v !== name));
        } else {
          onChange([...value, name].sort());
        }
      } else {
        if (selectedSet.has(name) && !required) {
          onChange([]);
        } else {
          onChange([name]);
          setOpen(false);
          setFilter("");
        }
      }
    },
    [multiple, onChange, required, selectedSet, value],
  );

  const clearAll = () => {
    if (required) return;
    onChange([]);
  };

  const selectAllFiltered = () => {
    if (!multiple) return;
    const names = new Set(value);
    filtered.forEach((r) => names.add(r.name));
    onChange([...names].sort());
  };

  function onKeyDown(e: KeyboardEvent) {
    if (!open && (e.key === "ArrowDown" || e.key === "Enter" || e.key === " ")) {
      e.preventDefault();
      setOpen(true);
      return;
    }
    if (!open) return;
    if (e.key === "Escape") {
      e.preventDefault();
      setOpen(false);
      setFilter("");
      return;
    }
    if (e.key === "ArrowDown") {
      e.preventDefault();
      setHighlight((h) => Math.min(h + 1, Math.max(0, filtered.length - 1)));
      return;
    }
    if (e.key === "ArrowUp") {
      e.preventDefault();
      setHighlight((h) => Math.max(h - 1, 0));
      return;
    }
    if (e.key === "Enter" && filtered[highlight]) {
      e.preventDefault();
      toggle(filtered[highlight].name);
    }
  }

  const summary =
    value.length === 0
      ? "All repositories"
      : value.length === 1
        ? value[0]
        : `${value.length} repositories selected`;

  return (
    <div className={`repo-picker ${className}`} ref={rootRef}>
      <div className="repo-picker__label-row">
        <span className="field__label" id={labelId}>
          {label}
          {required && (
            <span className="field__req" aria-hidden>
              *
            </span>
          )}
        </span>
        {error && (
          <button type="button" className="repo-picker__retry" onClick={() => refresh()}>
            Retry load
          </button>
        )}
      </div>

      <button
        type="button"
        className={`repo-picker__trigger field__input ${open ? "is-open" : ""}`}
        aria-haspopup="listbox"
        aria-expanded={open}
        aria-labelledby={labelId}
        aria-controls={listId}
        disabled={disabled || loading}
        onClick={() => !disabled && setOpen((o) => !o)}
        onKeyDown={onKeyDown}
      >
        <span className="repo-picker__summary">
          {loading ? "Loading repositories…" : error ? "Could not load repos" : summary}
        </span>
        <span className="repo-picker__chevron" aria-hidden>
          ▾
        </span>
      </button>

      {value.length > 0 && (
        <ul className="repo-picker__chips" aria-label="Selected repositories">
          {value.map((name) => (
            <li key={name}>
              <span className="repo-picker__chip">
                <span className="mono">{name}</span>
                <button
                  type="button"
                  className="repo-picker__chip-x"
                  aria-label={`Remove ${name}`}
                  onClick={() => toggle(name)}
                  disabled={required && value.length <= 1}
                >
                  ×
                </button>
              </span>
            </li>
          ))}
        </ul>
      )}

      {open && (
        <div className="repo-picker__panel" role="presentation">
          <div className="repo-picker__search-wrap">
            <label className="sr-only" htmlFor={`${listId}-search`}>
              Filter repositories
            </label>
            <input
              ref={searchRef}
              id={`${listId}-search`}
              className="field__input repo-picker__search"
              type="search"
              placeholder="Type to filter (e.g. payments, auth)…"
              value={filter}
              onChange={(e) => {
                setFilter(e.target.value);
                setHighlight(0);
              }}
              onKeyDown={onKeyDown}
              autoComplete="off"
            />
          </div>

          <div className="repo-picker__toolbar">
            <span className="repo-picker__count">
              {filtered.length === repos.length
                ? `${repos.length} repos`
                : `${filtered.length} of ${repos.length}`}
            </span>
            <div className="repo-picker__toolbar-actions">
              {multiple && filtered.length > 0 && (
                <button type="button" className="repo-picker__link-btn" onClick={selectAllFiltered}>
                  Select filtered
                </button>
              )}
              {!required && value.length > 0 && (
                <button type="button" className="repo-picker__link-btn" onClick={clearAll}>
                  Clear
                </button>
              )}
            </div>
          </div>

          <ul
            id={listId}
            className="repo-picker__list"
            role="listbox"
            aria-multiselectable={multiple}
            aria-label="Repository list"
          >
            {filtered.length === 0 && (
              <li className="repo-picker__empty" role="option" aria-disabled>
                {repos.length === 0
                  ? "No repositories indexed yet. Run the indexer first."
                  : `No repos match "${filter}".`}
              </li>
            )}
            {filtered.map((r, i) => (
              <RepoOptionRow
                key={r.name}
                option={r}
                selected={selectedSet.has(r.name)}
                highlighted={i === highlight}
                multiple={multiple}
                onToggle={() => toggle(r.name)}
                onHover={() => setHighlight(i)}
              />
            ))}
          </ul>
        </div>
      )}

      {hint && !error && <p className="field__hint">{hint}</p>}
      {error && (
        <p className="field__error" role="alert">
          {error}
        </p>
      )}
    </div>
  );
}

function RepoOptionRow({
  option,
  selected,
  highlighted,
  multiple,
  onToggle,
  onHover,
}: {
  option: RepoInfo;
  selected: boolean;
  highlighted: boolean;
  multiple: boolean;
  onToggle: () => void;
  onHover: () => void;
}) {
  return (
    <li
      role="option"
      aria-selected={selected}
      className={`repo-picker__option ${highlighted ? "is-hi" : ""} ${selected ? "is-sel" : ""}`}
      onMouseEnter={onHover}
    >
      <button type="button" className="repo-picker__option-btn" onClick={onToggle}>
        <span className="repo-picker__check" aria-hidden>
          {multiple ? (selected ? "☑" : "☐") : selected ? "●" : "○"}
        </span>
        <span className="repo-picker__name mono">{option.name}</span>
        <span className="repo-picker__nodes">{option.nodes.toLocaleString()} nodes</span>
      </button>
    </li>
  );
}
