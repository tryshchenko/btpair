// Decimal units, like Finder.
export function bytes(n: number | null | undefined): string {
  if (n == null) return "—";
  if (n < 1000) return `${n} B`;
  const units = ["kB", "MB", "GB", "TB", "PB"];
  let v = n;
  let i = -1;
  while (v >= 1000 && i < units.length - 1) {
    v /= 1000;
    i++;
  }
  return `${v >= 100 ? v.toFixed(0) : v.toFixed(1)} ${units[i]}`;
}

export function count(n: number | null | undefined): string {
  return n == null ? "—" : n.toLocaleString("en-US");
}

export function ago(iso: string | null | undefined): string {
  if (!iso) return "never";
  const s = (Date.now() - new Date(iso).getTime()) / 1000;
  if (s < 90) return "just now";
  if (s < 3600) return `${Math.round(s / 60)} min ago`;
  if (s < 86400 * 2) return `${Math.round(s / 3600)} h ago`;
  if (s < 86400 * 60) return `${Math.round(s / 86400)} days ago`;
  if (s < 86400 * 365 * 2) return `${Math.round(s / (86400 * 30))} months ago`;
  return `${Math.round(s / (86400 * 365))} years ago`;
}

export function ageDays(iso: string | null | undefined): number {
  return iso ? (Date.now() - new Date(iso).getTime()) / 86400000 : Infinity;
}

export function date(iso: string | null | undefined): string {
  return iso ? iso.slice(0, 10) : "—";
}

export function duration(sec: number): string {
  const s = Math.round(sec);
  if (s < 60) return `${s}s`;
  if (s < 3600) return `${Math.floor(s / 60)}m ${String(s % 60).padStart(2, "0")}s`;
  return `${Math.floor(s / 3600)}h ${String(Math.floor((s % 3600) / 60)).padStart(2, "0")}m`;
}

/** "1.5 GB" / "200mb" / "300" → bytes; null when empty or unparseable. */
export function parseSize(s: string): number | null {
  const m = s.trim().toLowerCase().match(/^([\d.]+)\s*(b|k|kb|m|mb|g|gb|t|tb)?$/);
  if (!m) return null;
  const mult: Record<string, number> = { b: 1, k: 1e3, kb: 1e3, m: 1e6, mb: 1e6, g: 1e9, gb: 1e9, t: 1e12, tb: 1e12 };
  return Math.round(parseFloat(m[1]) * (mult[m[2] || "b"] ?? 1));
}
