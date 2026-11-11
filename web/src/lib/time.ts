// "just now", "4 min ago", "3 h ago", "2 days ago": easier to scan than timestamps.
export function relativeTime(iso: string | null | undefined): string {
  if (!iso) return "";
  const secs = Math.round((Date.now() - Date.parse(iso)) / 1000);
  if (secs < 45) return "just now";
  const mins = Math.round(secs / 60);
  if (mins < 60) return `${mins} min ago`;
  const hours = Math.round(mins / 60);
  if (hours < 24) return `${hours} h ago`;
  const days = Math.round(hours / 24);
  return days === 1 ? "yesterday" : `${days} days ago`;
}

// How long a deployment took, e.g. "48s" or "2m 14s".
export function duration(start: string | null, end: string | null): string {
  if (!start || !end) return "";
  const secs = Math.max(0, Math.round((Date.parse(end) - Date.parse(start)) / 1000));
  return secs >= 60 ? `${Math.floor(secs / 60)}m ${secs % 60}s` : `${secs}s`;
}
