import { useMemo, useState, type ReactNode } from "react";

export interface Column<T> {
  header: string;
  render: (row: T) => ReactNode;
}

interface Props<T> {
  columns: Column<T>[];
  rows: T[];
  keyFn: (row: T, index: number) => string;
  // note is appended to the row-count line - pages use it to surface
  // truncation ("capped at 100, refine your query").
  note?: string;
}

type Dir = "asc" | "desc";

function isPrimitive(v: ReactNode): v is string | number {
  return typeof v === "string" || typeof v === "number";
}

// A small, explicit table: each page defines its own columns (header + render
// fn) instead of introspecting object keys, so column order and formatting
// (joining arrays, labels, etc.) stay under the page's control.
//
// Columns whose render() yields a plain string/number are click-sortable;
// component columns (badges) are not. A row-count line above the table makes
// result size and truncation visible without scrolling.
export function DataTable<T>({ columns, rows, keyFn, note }: Props<T>) {
  const [sortCol, setSortCol] = useState<number | null>(null);
  const [dir, setDir] = useState<Dir>("asc");

  const sortable = (ci: number): boolean =>
    rows.length > 0 && isPrimitive(columns[ci].render(rows[0]));

  const onSort = (ci: number) => {
    if (sortCol === ci) {
      setDir(dir === "asc" ? "desc" : "asc");
    } else {
      setSortCol(ci);
      setDir("asc");
    }
  };

  const sorted = useMemo(() => {
    if (sortCol === null || sortCol >= columns.length) return rows;
    const col = columns[sortCol];
    const val = (r: T): string | number => {
      const v = col.render(r);
      if (typeof v === "number") return v;
      if (typeof v === "string") return v.toLowerCase();
      return "";
    };
    return [...rows].sort((a, b) => {
      const va = val(a);
      const vb = val(b);
      if (va < vb) return dir === "asc" ? -1 : 1;
      if (va > vb) return dir === "asc" ? 1 : -1;
      return 0;
    });
  }, [rows, columns, sortCol, dir]);

  return (
    <>
      <p className="table-meta" role="status">
        {rows.length.toLocaleString()} result{rows.length === 1 ? "" : "s"}
        {note ? ` · ${note}` : ""}
      </p>
      <table className="data-table">
        <thead>
          <tr>
            {columns.map((c, i) => (
              <th
                key={c.header}
                aria-sort={
                  sortCol === i
                    ? dir === "asc"
                      ? "ascending"
                      : "descending"
                    : undefined
                }
              >
                {sortable(i) ? (
                  <button
                    type="button"
                    className="th-sort"
                    onClick={() => onSort(i)}
                    title={`sort by ${c.header}`}
                  >
                    {c.header}
                    {sortCol === i ? (dir === "asc" ? " ▲" : " ▼") : ""}
                  </button>
                ) : (
                  c.header
                )}
              </th>
            ))}
          </tr>
        </thead>
        <tbody>
          {sorted.map((row, i) => (
            <tr key={keyFn(row, i)}>
              {columns.map((c) => (
                <td key={c.header}>{c.render(row)}</td>
              ))}
            </tr>
          ))}
        </tbody>
      </table>
    </>
  );
}

export function joinList(xs?: string[]): string {
  return xs && xs.length > 0 ? xs.join(", ") : "-";
}

export function LabelBadges({ labels }: { labels: string[] }) {
  return (
    <>
      {labels
        .filter((l) => l !== "Entity")
        .map((l) => (
          <span key={l} className="badge">
            {l}
          </span>
        ))}
    </>
  );
}
