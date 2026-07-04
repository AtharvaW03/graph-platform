interface Props {
  loading: boolean;
  error: string | null;
  empty?: boolean;
  // emptyText explains WHY there are no results and what to try next -
  // pages pass context-aware text (e.g. mention an active repo scope).
  emptyText?: string;
}

export function StatusBox({ loading, error, empty, emptyText }: Props) {
  if (loading)
    return (
      <p className="status" role="status">
        Loading…
      </p>
    );
  if (error)
    return (
      <p className="status status-error" role="alert">
        Error: {error}
      </p>
    );
  if (empty)
    return (
      <p className="status" role="status">
        {emptyText ?? "No results."}
      </p>
    );
  return null;
}
