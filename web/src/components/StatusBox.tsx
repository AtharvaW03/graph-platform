interface Props {
  loading: boolean;
  error: string | null;
  empty?: boolean;
}

export function StatusBox({ loading, error, empty }: Props) {
  if (loading) return <p className="status">Loading…</p>;
  if (error) return <p className="status status-error">Error: {error}</p>;
  if (empty) return <p className="status">No results.</p>;
  return null;
}
