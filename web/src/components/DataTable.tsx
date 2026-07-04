import type { ReactNode } from "react";

export interface Column<T> {
  header: string;
  render: (row: T) => ReactNode;
}

interface Props<T> {
  columns: Column<T>[];
  rows: T[];
  keyFn: (row: T, index: number) => string;
}

// A small, explicit table: each page defines its own columns (header + render
// fn) instead of introspecting object keys, so column order and formatting
// (joining arrays, labels, etc.) stay under the page's control.
export function DataTable<T>({ columns, rows, keyFn }: Props<T>) {
  return (
    <table className="data-table">
      <thead>
        <tr>
          {columns.map((c) => (
            <th key={c.header}>{c.header}</th>
          ))}
        </tr>
      </thead>
      <tbody>
        {rows.map((row, i) => (
          <tr key={keyFn(row, i)}>
            {columns.map((c) => (
              <td key={c.header}>{c.render(row)}</td>
            ))}
          </tr>
        ))}
      </tbody>
    </table>
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
