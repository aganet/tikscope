// Time ranges: presets, the absolute window a range covers, and the earlier
// window a comparison line is drawn from.
export const PRESET_SECS = { "1h": 3600, "6h": 21600, "24h": 86400, "7d": 604800, "30d": 2592000 };
export const COMPARE = { "1d": { secs: 86400, label: "yesterday" }, "7d": { secs: 604800, label: "last week" } };

export function rangeWindow(range, now = Math.floor(Date.now() / 1000)) {
  const [a, b] = range.split("..");
  if (b !== undefined) return { from: Number(a), to: Number(b) };
  return { from: now - (PRESET_SECS[range] || 86400), to: now };
}

export function compareRange(state) {
  const c = COMPARE[state.compare];
  if (!c) return null;
  const { from, to } = rangeWindow(state.range);
  return `${from - c.secs}..${to - c.secs}`;
}

export function compareLabel(state) {
  return COMPARE[state.compare] ? COMPARE[state.compare].label : "";
}
