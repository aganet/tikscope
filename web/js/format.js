const compact = new Intl.NumberFormat(undefined, { notation: "compact", maximumFractionDigits: 1 });
const plain = new Intl.NumberFormat();

export const fmtNum = (n) => (n >= 10000 ? compact.format(n) : plain.format(n));
export const fmtInt = (n) => plain.format(Math.round(n));

export function fmtPct(part, whole, digits = 1) {
  if (!whole) return "0%";
  return `${((part / whole) * 100).toFixed(digits)}%`;
}

export function fmtMs(ms) {
  if (ms === null || ms === undefined || Number.isNaN(ms)) return "-";
  if (ms >= 1000) return `${(ms / 1000).toFixed(2)} s`;
  if (ms < 10) return `${ms.toFixed(1)} ms`;
  return `${Math.round(ms)} ms`;
}

export function fmtBytes(n) {
  const units = ["B", "KiB", "MiB", "GiB", "TiB"];
  let i = 0;
  let v = n;
  while (v >= 1024 && i < units.length - 1) { v /= 1024; i++; }
  return `${v.toFixed(i === 0 ? 0 : 1)} ${units[i]}`;
}

export function fmtBps(bps) {
  if (bps === null || bps === undefined) return "-";
  const units = ["bps", "kbps", "Mbps", "Gbps"];
  let i = 0;
  let v = bps;
  while (v >= 1000 && i < units.length - 1) { v /= 1000; i++; }
  return `${v.toFixed(v >= 100 || i === 0 ? 0 : 1)} ${units[i]}`;
}

export function fmtDateTime(ts) {
  return new Date(ts * 1000).toLocaleString([], { month: "short", day: "2-digit", hour: "2-digit", minute: "2-digit", second: "2-digit" });
}

export function fmtTime(ts) {
  return new Date(ts * 1000).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" });
}

export function fmtAgo(ts, now = Date.now()) {
  const s = Math.max(0, Math.round(now / 1000 - ts));
  if (s < 60) return `${s}s ago`;
  if (s < 3600) return `${Math.floor(s / 60)}m ago`;
  if (s < 86400) return `${Math.floor(s / 3600)}h ago`;
  return `${Math.floor(s / 86400)}d ago`;
}

export function fmtDuration(secs) {
  const d = Math.floor(secs / 86400), hh = Math.floor((secs % 86400) / 3600), m = Math.floor((secs % 3600) / 60);
  if (d > 0) return `${d}d ${hh}h`;
  if (hh > 0) return `${hh}h ${m}m`;
  return `${m}m ${secs % 60}s`;
}

// Axis tick formatter that adapts to the bucket size of a timeline.
export function tickFormatter(bucketSecs) {
  if (bucketSecs >= 3600) return (ts) => new Date(ts * 1000).toLocaleString([], { weekday: "short", hour: "2-digit", minute: "2-digit" });
  return fmtTime;
}

export function bucketLabel(ts, bucketSecs) {
  const day = bucketSecs >= 3600 ? new Date(ts * 1000).toLocaleDateString([], { weekday: "short", day: "2-digit", month: "short" }) + " " : "";
  return `${day}${fmtTime(ts)} - ${fmtTime(ts + bucketSecs)}`;
}

export const outcomeLabel = { ok: "Resolved", nodata: "No data", nxdomain: "NXDOMAIN", servfail: "SERVFAIL", blocked: "Blocked", error: "Error", "": "No reply" };

// Outcome groups use the reserved status palette and ship with an icon and a
// label, so colour never carries the meaning alone.
export const outcomes = [
  { key: "resolved", label: "Resolved", color: "var(--good)", icon: "check", filter: "resolved" },
  { key: "nxdomain", label: "NXDOMAIN", color: "var(--warning)", icon: "help", filter: "nxdomain" },
  { key: "blocked", label: "Blocked", color: "var(--serious)", icon: "ban", filter: "blocked" },
  { key: "failed", label: "Failed", color: "var(--critical)", icon: "x", filter: "failed" },
  { key: "unknown", label: "No reply", color: "var(--neutral)", icon: "minus", filter: "unknown" },
];

export function outcomeOf(o) {
  if (o === "ok" || o === "nodata") return "resolved";
  if (o === "nxdomain" || o === "blocked") return o;
  if (o === "servfail" || o === "error") return "failed";
  return "unknown";
}

export function outcomeTotals(counts) {
  const t = { resolved: 0, nxdomain: 0, blocked: 0, failed: 0, unknown: 0 };
  for (const [k, n] of Object.entries(counts || {})) t[outcomeOf(k)] += n;
  return t;
}

export const severityColor = { critical: "var(--critical)", error: "var(--critical)", warning: "var(--warning)", info: "var(--neutral)" };
