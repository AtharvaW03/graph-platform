// formatAge renders seconds as a short human age: "just now", "12m", "3h 20m".
export function formatAge(seconds: number): string {
  if (seconds < 90) return "just now";
  const m = Math.floor(seconds / 60);
  if (m < 60) return `${m}m`;
  const h = Math.floor(m / 60);
  if (h < 48) return `${h}h ${m % 60}m`;
  return `${Math.floor(h / 24)}d`;
}
