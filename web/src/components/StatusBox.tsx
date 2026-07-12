import { Alert, EmptyState, Skeleton } from "./ui";

interface Props {
  loading: boolean;
  error: string | null;
  empty?: boolean;
  // emptyText explains WHY there are no results and what to try next -
  // pages pass context-aware text (e.g. mention an active repo scope).
  emptyText?: string;
}

export function StatusBox({ loading, error, empty, emptyText }: Props) {
  if (loading) return <Skeleton rows={5} />;
  if (error)
    return (
      <Alert tone="danger" title="Request failed">
        {error}
      </Alert>
    );
  if (empty)
    return (
      <EmptyState
        title="No results"
        description={emptyText ?? "No results."}
      />
    );
  return null;
}
