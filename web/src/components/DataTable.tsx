import { useEffect, useMemo, useState, type ReactNode } from "react";
import { Badge } from "./ui";

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

// pageSize balances scanability against pager churn; result sets are
// server-capped at 100-1000 rows, so at most ~20 pages.
const pageSize = 50;

function isPrimitive(v: ReactNode): v is string | number {
  return typeof v === "string" || typeof v === "number";
}

// pageWindow returns the Google-style page-number strip: first and last
// always visible, a window around the current page, gaps as "...".
function pageWindow(current: number, total: number): (number | "...")[] {
  if (total <= 7) {
    return Array.from({ length: total }, (_, i) => i + 1);
  }
  const wanted = new Set<number>([
    1,
    total,
    current - 1,
    current,
    current + 1,
  ]);
  const pages = [...wanted]
    .filter((p) => p >= 1 && p <= total)
    .sort((a, b) => a - b);
  const out: (number | "...")[] = [];
  let prev = 0;
  for (const p of pages) {
    if (p - prev === 2) out.push(prev + 1);
    else if (p - prev > 2) out.push("...");
    out.push(p);
    prev = p;
  }
  return out;
}

// A small, explicit table: each page defines its own columns (header + render
// fn) instead of introspecting object keys, so column order and formatting
// (joining arrays, labels, etc.) stay under the page's control.
//
// Columns whose render() yields a plain string/number are click-sortable;
// component columns (badges) are not. A row-count line above the table makes
// result size and truncation visible without scrolling, and result sets
// longer than pageSize get a pager below the table.
export function DataTable<T>({ columns, rows, keyFn, note }: Props<T>) {
  const [sortCol, setSortCol] = useState<number | null>(null);
  const [dir, setDir] = useState<Dir>("asc");
  const [page, setPage] = useState(1);

  // New result set: back to page 1 (a stale page index would show nothing).
  useEffect(() => {
    setPage(1);
  }, [rows]);

  const sortable = (ci: number): boolean =>
    rows.length > 0 && isPrimitive(columns[ci].render(rows[0]));

  const onSort = (ci: number) => {
    if (sortCol === ci) {
      setDir(dir === "asc" ? "desc" : "asc");
    } else {
      setSortCol(ci);
      setDir("asc");
    }
    setPage(1);
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

  const totalPages = Math.max(1, Math.ceil(sorted.length / pageSize));
  const safePage = Math.min(page, totalPages);
  const start = (safePage - 1) * pageSize;
  const visible = sorted.slice(start, start + pageSize);

  return (
    <>
      <p className="table-meta" role="status">
        {rows.length.toLocaleString()} result{rows.length === 1 ? "" : "s"}
        {totalPages > 1
          ? ` · showing ${start + 1}-${start + visible.length}`
          : ""}
        {note ? ` · ${note}` : ""}
      </p>
      <div className="table-wrap" role="region" aria-label="Results table">
        <table className="table">
          <thead>
            <tr>
              {columns.map((c, i) => (
                <th
                  key={c.header}
                  scope="col"
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
            {visible.map((row, i) => (
              <tr key={keyFn(row, start + i)}>
                {columns.map((c) => (
                  <td key={c.header}>{c.render(row)}</td>
                ))}
              </tr>
            ))}
          </tbody>
        </table>
      </div>
      {totalPages > 1 && (
        <nav className="pager" aria-label="Result pages">
          <button
            type="button"
            onClick={() => setPage(safePage - 1)}
            disabled={safePage === 1}
          >
            ‹ Prev
          </button>
          {pageWindow(safePage, totalPages).map((p, i) =>
            p === "..." ? (
              <span key={`gap-${i}`} className="pager-gap">
                …
              </span>
            ) : (
              <button
                type="button"
                key={p}
                onClick={() => setPage(p)}
                aria-current={p === safePage ? "page" : undefined}
              >
                {p}
              </button>
            ),
          )}
          <button
            type="button"
            onClick={() => setPage(safePage + 1)}
            disabled={safePage === totalPages}
          >
            Next ›
          </button>
        </nav>
      )}
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
          <Badge key={l}>{l}</Badge>
        ))}
    </>
  );
}

// Plain-language label + tone + the technical tier in a hover hint, per the
// UI convention of keeping jargon out of the visible text. An unknown/empty
// confidence renders nothing rather than a misleading badge.
const CONFIDENCE_DISPLAY: Record<
  string,
  { label: string; tone: "success" | "warning" | "danger"; hint: string }
> = {
  EXTRACTED: {
    label: "Explicit",
    tone: "success",
    hint: "EXTRACTED — stated directly in the source (e.g. an import or a direct call). Near-certain.",
  },
  INFERRED: {
    label: "Inferred",
    tone: "warning",
    hint: "INFERRED — deduced heuristically (e.g. a regex match or a call-graph second pass). Treat as a lead, not a proof.",
  },
  AMBIGUOUS: {
    label: "Ambiguous",
    tone: "danger",
    hint: "AMBIGUOUS — the extractor was unsure; low confidence.",
  },
};

export function ConfidenceBadge({ confidence }: { confidence?: string }) {
  const d = confidence ? CONFIDENCE_DISPLAY[confidence] : undefined;
  if (!d) return <>-</>;
  return (
    <span title={d.hint}>
      <Badge tone={d.tone}>{d.label}</Badge>
    </span>
  );
}
