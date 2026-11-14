// Hand-rolled SVG charts: stacked columns, multi-series lines and sparklines.
// Marks are thin, grids are hairlines, and every chart carries a hover layer.
import { h, clear } from "./dom.js";
import { fmtNum, fmtAgo, tickFormatter, bucketLabel } from "./format.js";

const PAD = { top: 8, right: 8, bottom: 22, left: 48 };
const tooltipEl = () => document.getElementById("tooltip");

let lastPointer = 0, dragging = false;

// True while the pointer is on a chart, so a refresh does not sweep away a
// tooltip or a selection in progress.
export function interacting() {
  return dragging || (!tooltipEl().hidden && Date.now() - lastPointer < 5000);
}

function showTooltip(x, y, title, rows) {
  lastPointer = Date.now();
  const el = tooltipEl();
  clear(el);
  el.appendChild(h("div", { class: "t" }, title));
  for (const r of rows) {
    el.appendChild(h("div", { class: "r" },
      h("span", { style: r.color ? { color: r.color } : null }, r.color ? h("i") : null, r.label),
      h("b", { class: "num" }, r.value)));
  }
  el.hidden = false;
  const w = el.offsetWidth, hh = el.offsetHeight;
  let left = x + 14, top = y + 14;
  if (left + w > window.innerWidth - 8) left = x - w - 14;
  if (top + hh > window.innerHeight - 8) top = y - hh - 14;
  el.style.left = `${left}px`;
  el.style.top = `${top}px`;
}

export function hideTooltip() {
  tooltipEl().hidden = true;
}

// Dragging across a plot zooms into that span and a double click zooms back
// out; the app decides what both mean, charts only report them.
let zoom = null;
export function onZoom(handlers) {
  zoom = handlers;
}

function brush(svg, hover, { width, plotW, plotH, times, bucketSecs }) {
  if (!zoom || times.length < 2) return;
  const n = times.length;
  const sel = h("svg:rect", { class: "brush", y: PAD.top, height: plotH, style: { display: "none" } });
  svg.insertBefore(sel, hover);
  const px = (e) => {
    const box = svg.getBoundingClientRect();
    return Math.max(PAD.left, Math.min(PAD.left + plotW, ((e.clientX - box.left) / box.width) * width));
  };
  const index = (x) => Math.max(0, Math.min(n - 1, Math.floor(((x - PAD.left) / plotW) * n)));
  let x0 = null;
  hover.addEventListener("pointerdown", (e) => {
    if (e.button !== 0) return;
    x0 = px(e);
    dragging = true;
    hover.setPointerCapture(e.pointerId);
  });
  hover.addEventListener("pointermove", (e) => {
    if (x0 === null) return;
    const x1 = px(e);
    sel.setAttribute("x", Math.min(x0, x1));
    sel.setAttribute("width", Math.abs(x1 - x0));
    sel.style.display = "";
    hideTooltip();
  });
  const cancel = () => { x0 = null; dragging = false; sel.style.display = "none"; };
  hover.addEventListener("pointerup", (e) => {
    if (x0 === null) return;
    const a = x0, b = px(e);
    cancel();
    // A plain click is not a selection.
    if (Math.abs(b - a) < 6) return;
    const i0 = index(Math.min(a, b)), i1 = index(Math.max(a, b));
    zoom.into(times[i0], times[i1] + bucketSecs);
  });
  hover.addEventListener("pointercancel", cancel);
  hover.addEventListener("dblclick", () => zoom.out());
}

// A round tick step of about a quarter of the value, 1, 2, 2.5 or 5 times a
// power of ten.
function niceStep(v) {
  const p = Math.pow(10, Math.floor(Math.log10(v)));
  const n = v / p;
  return (n <= 1 ? 1 : n <= 2 ? 2 : n <= 2.5 ? 2.5 : n <= 5 ? 5 : 10) * p;
}

// The axis top: the first tick above the peak, so the data fills the plot.
export function niceMax(v) {
  if (v <= 0) return 1;
  const step = niceStep(v / 4);
  return Math.ceil(v / step - 1e-9) * step;
}

// A chart height is a pixel count or a function of the container, see fillHeight.
function resolveHeight(height, container) {
  return typeof height === "function" ? height(container) : height;
}

// Height for a chart that fills a flex container, minus room for a legend.
// Falls back to a fixed height when the container has no definite height.
export function fillHeight(reserve = 0, fallback = 200) {
  return (el) => (el.clientHeight ? Math.max(40, el.clientHeight - reserve) : fallback);
}

// One label per tick step, thinned on short charts so they never overlap.
function yTicks(max, plotH) {
  const step = niceStep(max / 4);
  const all = [];
  for (let t = 0; t <= max + step / 2; t += step) all.push(Math.min(t, max));
  const room = plotH < 24 ? 1 : plotH < 50 ? 2 : plotH < 110 ? 3 : 5;
  if (all.length <= room) return all;
  if (room === 1) return [max];
  const every = Math.ceil((all.length - 1) / (room - 1));
  return all.filter((_, i) => i % every === 0 || i === all.length - 1);
}

// A y axis: tick values and the pixel row of any value, clamped to the plot.
function linearAxis(maxY, plotH) {
  return { ticks: yTicks(maxY, plotH), y: (v) => PAD.top + plotH - (Math.max(0, Math.min(v, maxY)) / maxY) * plotH };
}

// Log axis from the decade below the smallest value (at most three decades
// under the top) so bursts and the usual traffic are both readable.
function logAxis(maxY, minV, plotH) {
  const top = niceStep(maxY);
  const floor = Math.max(top / 1000, Math.pow(10, Math.floor(Math.log10(Math.max(minV, 1e-9)))));
  const lt = Math.log10(top), lf = Math.log10(floor);
  const ticks = [];
  for (let t = floor; t < top * 0.999; t *= 10) ticks.push(t);
  ticks.push(top);
  const room = plotH < 24 ? 1 : plotH < 50 ? 2 : plotH < 110 ? 3 : 5;
  const every = Math.ceil(ticks.length / room);
  return {
    ticks: ticks.filter((_, i) => i % every === 0 || i === ticks.length - 1),
    y: (v) => PAD.top + plotH - Math.max(0, Math.min(1, (Math.log10(Math.max(v, floor)) - lf) / (lt - lf))) * plotH,
  };
}

function frame(width, height, bucketSecs, times, yAxis, yFormat) {
  const svg = h("svg:svg", { viewBox: `0 0 ${width} ${height}`, width, height });
  const plotW = width - PAD.left - PAD.right, plotH = height - PAD.top - PAD.bottom;
  const grid = h("svg:g", { class: "grid" });
  const axis = h("svg:g", { class: "axis" });
  for (const t of yAxis.ticks) {
    const y = yAxis.y(t);
    grid.appendChild(h("svg:line", { x1: PAD.left, x2: PAD.left + plotW, y1: y, y2: y }));
    axis.appendChild(h("svg:text", { x: PAD.left - 6, y: y + 4, "text-anchor": "end" }, yFormat(t)));
  }
  const n = times.length;
  const tf = tickFormatter(bucketSecs);
  const step = Math.max(1, Math.ceil(n / Math.max(1, Math.floor(plotW / 90))));
  for (let i = 0; i < n; i += step) {
    const x = PAD.left + ((i + 0.5) / n) * plotW;
    axis.appendChild(h("svg:text", { x, y: height - 6, "text-anchor": "middle" }, tf(times[i])));
  }
  svg.appendChild(grid);
  svg.appendChild(axis);
  return { svg, plotW, plotH };
}

export function legend(series, line = false) {
  return h("div", { class: "legend" }, series.map((s) => h("span", null,
    h("i", { class: s.dash ? "line dash" : line ? "line" : "", style: s.dash ? { borderColor: s.color } : { background: s.color } }), s.label)));
}

// Points of an earlier window moved onto the current one's timestamps, so a
// comparison line shares the x axis. Buckets with no counterpart are gaps.
export function alignedPoints(buckets, previous, shiftSecs, field = "avg") {
  const byTS = new Map((previous || []).map((b) => [b.ts + shiftSecs, b[field]]));
  return buckets.map((b) => ({ ts: b.ts, v: byTS.has(b.ts) ? byTS.get(b.ts) : null }));
}

// Stacked columns. buckets: [{ts, ...}], series: [{key, label, color}].
// overlay: {label, color, points} draws a dashed line over the bars, for the
// same window in an earlier period.
export function stackedBars(container, { buckets, bucketSecs, series, height = 220, yFormat = fmtNum, valueFormat = fmtNum, overlay = null }) {
  clear(container);
  container.classList.add("chart");
  const width = Math.max(120, container.clientWidth || 600);
  height = resolveHeight(height, container);
  if (!buckets.length) {
    container.appendChild(h("div", { class: "chart-empty" }, "No data in this range"));
    return;
  }
  const totals = buckets.map((b) => series.reduce((s, k) => s + (b[k.key] || 0), 0));
  const overlayVals = overlay ? overlay.points.map((p) => p.v).filter((v) => v !== null && v !== undefined) : [];
  const maxY = niceMax(Math.max(...totals, ...overlayVals));
  const plotH0 = height - PAD.top - PAD.bottom;
  const { svg, plotW, plotH } = frame(width, height, bucketSecs, buckets.map((b) => b.ts), linearAxis(maxY, plotH0), yFormat);
  const n = buckets.length;
  const slot = plotW / n;
  const barW = Math.max(1, Math.min(24, slot - 2));
  const bars = h("svg:g");
  const hover = h("svg:g", { class: "hover" });
  buckets.forEach((b, i) => {
    const x = PAD.left + i * slot + (slot - barW) / 2;
    let y = PAD.top + plotH;
    for (const s of series) {
      const v = b[s.key] || 0;
      if (v <= 0) continue;
      const hh = (v / maxY) * plotH;
      y -= hh;
      bars.appendChild(h("svg:rect", { x, y, width: barW, height: Math.max(0, hh - (y > PAD.top ? 1 : 0)), fill: s.color }));
    }
    const rect = h("svg:rect", { x: PAD.left + i * slot, y: PAD.top, width: slot, height: plotH });
    rect.addEventListener("pointermove", (e) => showTooltip(e.clientX, e.clientY, bucketLabel(b.ts, bucketSecs), [
      { label: "Total", value: valueFormat(totals[i]) },
      ...series.map((s) => ({ label: s.label, value: valueFormat(b[s.key] || 0), color: s.color })),
      ...(overlay ? [{ label: overlay.label, value: overlay.points[i] && overlay.points[i].v !== null ? valueFormat(overlay.points[i].v) : "-", color: overlay.color }] : []),
    ]));
    rect.addEventListener("pointerleave", hideTooltip);
    hover.appendChild(rect);
  });
  svg.appendChild(bars);
  if (overlay) {
    let d = "", open = false;
    overlay.points.forEach((p, i) => {
      if (p.v === null || p.v === undefined) { open = false; return; }
      d += `${open ? " L" : " M"}${(PAD.left + (i + 0.5) * slot).toFixed(1)} ${(PAD.top + plotH - (Math.min(p.v, maxY) / maxY) * plotH).toFixed(1)}`;
      open = true;
    });
    svg.appendChild(h("svg:path", { d, fill: "none", stroke: overlay.color, "stroke-width": "2", "stroke-dasharray": "5 4", "stroke-opacity": "0.85", "stroke-linejoin": "round" }));
  }
  svg.appendChild(hover);
  brush(svg, hover, { width, plotW, plotH, times: buckets.map((b) => b.ts), bucketSecs });
  container.appendChild(svg);
}

// Lines. series: [{label, color, points: [{ts, v}]}], v may be null for gaps.
// band draws each bucket's peak as a thin line with shading under it, and the
// axis reaches the peaks. scale: "linear", "log", or "auto", which picks log
// when the peaks are more than eight times the typical level, so a burst and
// the usual traffic are both readable.
export function lines(container, { series, bucketSecs, height = 200, yFormat = fmtNum, valueFormat = fmtNum, area = false, maxY: fixedMax, band = false, scale = "linear" }) {
  clear(container);
  container.classList.add("chart");
  const width = Math.max(120, container.clientWidth || 600);
  height = resolveHeight(height, container);
  const times = series[0] ? series[0].points.map((p) => p.ts) : [];
  const avgs = series.flatMap((s) => s.points.map((p) => p.v)).filter((v) => v !== null && v !== undefined);
  const peaks = band ? series.flatMap((s) => s.points.map((p) => p.max)).filter((v) => v !== null && v !== undefined) : [];
  if (!times.length || !avgs.length) {
    container.appendChild(h("div", { class: "chart-empty" }, "No data in this range"));
    return;
  }
  const maxAvg = Math.max(...avgs), maxPeak = Math.max(maxAvg, ...peaks);
  const maxY = fixedMax || niceMax(maxPeak);
  const positive = avgs.filter((v) => v > 0);
  const log = scale === "log" || (scale === "auto" && positive.length > 0 && maxPeak > 8 * maxAvg);
  const plotH0 = height - PAD.top - PAD.bottom;
  const yAxis = log ? logAxis(maxY, Math.min(...positive), plotH0) : linearAxis(maxY, plotH0);
  const { svg, plotW, plotH } = frame(width, height, bucketSecs, times, yAxis, yFormat);
  const n = times.length;
  const xAt = (i) => PAD.left + ((i + 0.5) / n) * plotW;
  const yAt = yAxis.y;
  if (band) {
    for (const s of series) {
      let d = "", open = false;
      const peaks = bridged(s.points.map((p) => ({ v: p.max })));
      s.points.forEach((p, i) => {
        if (peaks[i] === null) { if (open) d += ` L${xAt(i - 1)} ${PAD.top + plotH} Z`; open = false; return; }
        if (!open) { d += `M${xAt(i)} ${PAD.top + plotH} L${xAt(i)} ${yAt(peaks[i])}`; open = true; } else d += ` L${xAt(i)} ${yAt(peaks[i])}`;
      });
      if (open) d += ` L${xAt(s.points.length - 1)} ${PAD.top + plotH} Z`;
      svg.appendChild(h("svg:path", { d, fill: s.color, "fill-opacity": "0.1" }));
      svg.appendChild(h("svg:path", { d: d.replace(/ L[\d.]+ [\d.]+ Z/g, "").replace(/M([\d.]+) [\d.]+ L\1 /g, "M$1 "), fill: "none", stroke: s.color, "stroke-width": "1", "stroke-opacity": "0.55", "stroke-linejoin": "round" }));
    }
  }
  for (const s of series) {
    let d = "", areaD = "", start = null;
    const drawn = bridged(s.points);
    s.points.forEach((p, i) => {
      if (drawn[i] === null) {
        if (start !== null) { areaD += ` L${xAt(i - 1)} ${PAD.top + plotH} Z`; start = null; }
        return;
      }
      const x = xAt(i), y = yAt(drawn[i]);
      if (start === null) { d += `M${x} ${y}`; areaD += `M${x} ${PAD.top + plotH} L${x} ${y}`; start = i; }
      else { d += ` L${x} ${y}`; areaD += ` L${x} ${y}`; }
    });
    if (start !== null) areaD += ` L${xAt(s.points.length - 1)} ${PAD.top + plotH} Z`;
    if (area && series.length === 1) svg.appendChild(h("svg:path", { d: areaD, fill: s.color, "fill-opacity": "0.1" }));
    svg.appendChild(h("svg:path", { d, fill: "none", stroke: s.color, "stroke-width": "2", "stroke-linejoin": "round", "stroke-linecap": "round",
      "stroke-dasharray": s.dash ? "5 4" : null, "stroke-opacity": s.dash ? "0.85" : null }));
  }
  const cross = h("svg:line", { class: "crosshair", y1: PAD.top, y2: PAD.top + plotH, style: { display: "none" } });
  const dots = series.map((s) => h("svg:circle", { r: 4, fill: s.color, stroke: "var(--surface)", "stroke-width": "2", style: { display: "none" } }));
  svg.appendChild(cross);
  dots.forEach((d) => svg.appendChild(d));
  const hover = h("svg:rect", { class: "hover", x: PAD.left, y: PAD.top, width: plotW, height: plotH, fill: "transparent" });
  hover.addEventListener("pointermove", (e) => {
    const box = svg.getBoundingClientRect();
    const px = ((e.clientX - box.left) / box.width) * width;
    const i = Math.max(0, Math.min(n - 1, Math.floor(((px - PAD.left) / plotW) * n)));
    const x = xAt(i);
    cross.setAttribute("x1", x);
    cross.setAttribute("x2", x);
    cross.style.display = "";
    const rows = series.map((s, k) => {
      const v = s.points[i] ? s.points[i].v : null;
      if (v === null || v === undefined) dots[k].style.display = "none";
      else { dots[k].setAttribute("cx", x); dots[k].setAttribute("cy", yAt(v)); dots[k].style.display = ""; }
      const peak = band && s.points[i] && s.points[i].max !== null && s.points[i].max !== undefined ? ` (peak ${valueFormat(s.points[i].max)})` : "";
      return { label: s.label, value: v === null || v === undefined ? "-" : valueFormat(v) + peak, color: s.color };
    });
    showTooltip(e.clientX, e.clientY, bucketLabel(times[i], bucketSecs), rows);
  });
  hover.addEventListener("pointerleave", () => { cross.style.display = "none"; dots.forEach((d) => (d.style.display = "none")); hideTooltip(); });
  svg.appendChild(hover);
  brush(svg, hover, { width, plotW, plotH, times, bucketSecs });
  container.appendChild(svg);
}

// One empty bucket between two filled ones is a missed poll, not an outage:
// the line crosses it. Two or more in a row stay a gap. The tooltip still
// shows the bucket as empty.
function bridged(points) {
  const v = points.map((p) => (p.v === null || p.v === undefined ? null : p.v));
  return v.map((x, i) => (x === null && i > 0 && i < v.length - 1 && v[i - 1] !== null && v[i + 1] !== null ? (v[i - 1] + v[i + 1]) / 2 : x));
}

// A recent-trend sparkline: the last 30 real samples, evenly spaced.
export function sparkline(points, color, width = 120, height = 28) {
  const vals = points.filter((p) => p !== null && p !== undefined).slice(-30);
  const present = vals;
  // Fewer than three points cannot show a trend and only leave stray marks.
  if (present.length < 3) return null;
  const svg = h("svg:svg", { class: "sparkline", viewBox: `0 0 ${width} ${height}`, preserveAspectRatio: "none", "aria-hidden": "true" });
  const max = Math.max(...present, 1), n = vals.length;
  let d = "", open = false;
  vals.forEach((v, i) => {
    if (v === null) { open = false; return; }
    const x = (i / Math.max(1, n - 1)) * width, y = height - 2 - (v / max) * (height - 4);
    d += `${open ? " L" : " M"}${x.toFixed(1)} ${y.toFixed(1)}`;
    open = true;
  });
  svg.appendChild(h("svg:path", { d, fill: "none", stroke: color, "stroke-width": "1.5", "stroke-linejoin": "round", "vector-effect": "non-scaling-stroke" }));
  return svg;
}

export function bucketsToPoints(buckets, field = "avg") {
  return buckets.map((b) => ({ ts: b.ts, v: b[field] === undefined ? null : b[field], max: b.max === undefined ? null : b.max }));
}

// draw runs after the current DOM update has been attached and laid out, so
// charts measure their real container width.
export function draw(fn) {
  requestAnimationFrame(fn);
}

// Speedometer: a 270 degree arc opening at the bottom, the live value in the
// centre, major ticks labelled outside the arc and a marker at the tip.
// Peak hold like a transceiver's meter: the marker sits at the highest value
// for HOLD_MS, then falls back to the needle over DECAY_MS. Returns null once
// it has caught up.
const HOLD_MS = 15 * 1000, DECAY_MS = 45 * 1000;
const peaks = new Map();
export function peakHold(key, value, now = Date.now()) {
  const v = value === null || value === undefined ? 0 : value;
  const held = peaks.get(key);
  if (!held || v >= held.top) {
    peaks.set(key, { top: v, at: now });
    return null;
  }
  const fall = Math.max(0, now - held.at - HOLD_MS) / DECAY_MS;
  const marker = held.top - (held.top - v) * Math.min(1, fall);
  if (marker <= v) {
    peaks.set(key, { top: v, at: now });
    return null;
  }
  return { value: marker, top: held.top, at: held.at };
}

export function gauge(container, { value, max, label, color, format, sub, peak }) {
  clear(container);
  container.classList.add("gauge");
  const W = 320, H = 250, cx = 160, cy = 130, r = 90, stroke = 14;
  const tf = (x) => format(x).replace(/\.0 /, " ");
  const start = 135, end = 405;
  const rad = (deg) => (deg * Math.PI) / 180;
  const point = (deg, rr) => [cx + rr * Math.cos(rad(deg)), cy + rr * Math.sin(rad(deg))];
  const arc = (from, to, rr) => {
    const [x1, y1] = point(from, rr), [x2, y2] = point(to, rr);
    return `M${x1.toFixed(2)} ${y1.toFixed(2)} A${rr} ${rr} 0 ${to - from > 180 ? 1 : 0} 1 ${x2.toFixed(2)} ${y2.toFixed(2)}`;
  };
  const v = value === null || value === undefined ? 0 : value;
  const frac = Math.max(0, Math.min(1, max > 0 ? v / max : 0));
  const angle = start + frac * (end - start);
  const svg = h("svg:svg", { viewBox: `0 0 ${W} ${H}`, width: W, height: H, role: "img", "aria-label": `${label} ${format(v)} of ${format(max)}` });
  const gid = `g${Math.random().toString(36).slice(2, 8)}`;
  const defs = h("svg:defs");
  const grad = h("svg:linearGradient", { id: gid, x1: "0", y1: "1", x2: "1", y2: "0" });
  grad.appendChild(h("svg:stop", { offset: "0", "stop-color": color, "stop-opacity": "0.55" }));
  grad.appendChild(h("svg:stop", { offset: "1", "stop-color": color }));
  defs.appendChild(grad);
  svg.appendChild(defs);
  svg.appendChild(h("svg:path", { d: arc(start, end, r), fill: "none", stroke: "var(--surface-2)", "stroke-width": stroke, "stroke-linecap": "round" }));
  // Major ticks on the same round step as a chart axis, four minors between.
  const step = max > 0 ? niceStep(max / 4) : 1;
  const majors = Math.max(1, Math.round(max / step));
  for (let i = 0; i <= majors * 4; i++) {
    const a = start + (i / (majors * 4)) * (end - start);
    const major = i % 4 === 0;
    const [x1, y1] = point(a, r + stroke / 2 + 4), [x2, y2] = point(a, r + stroke / 2 + (major ? 11 : 7));
    svg.appendChild(h("svg:line", { x1, y1, x2, y2, stroke: "var(--muted)", "stroke-width": major ? "1.5" : "1", "stroke-opacity": major ? "1" : "0.6" }));
    if (major) {
      const [tx, ty] = point(a, r + stroke / 2 + 24);
      const c = Math.cos(rad(a));
      const anchor = Math.abs(c) < 0.3 ? "middle" : c < 0 ? "end" : "start";
      svg.appendChild(h("svg:text", { x: tx, y: ty + 4, "text-anchor": anchor, class: "tick" }, tf((i / (majors * 4)) * max)));
    }
  }
  // The marker is always drawn so an idle link still shows where the needle is.
  if (frac > 0.002) svg.appendChild(h("svg:path", { d: arc(start, angle, r), fill: "none", stroke: `url(#${gid})`, "stroke-width": stroke, "stroke-linecap": "round" }));
  if (peak && peak.value > v) {
    const pa = start + Math.max(0, Math.min(1, max > 0 ? peak.value / max : 0)) * (end - start);
    const [x1, y1] = point(pa, r - stroke / 2 - 2), [x2, y2] = point(pa, r + stroke / 2 + 2);
    svg.appendChild(h("svg:line", { x1, y1, x2, y2, stroke: color, "stroke-width": "3", "stroke-linecap": "round" }));
  }
  const [mx, my] = point(angle, r);
  svg.appendChild(h("svg:circle", { cx: mx, cy: my, r: stroke / 2 + 3, fill: "var(--surface)", stroke: color, "stroke-width": "3" }));
  svg.appendChild(h("svg:text", { x: cx, y: cy + 2, "text-anchor": "middle", class: "value" }, format(v).split(" ")[0]));
  svg.appendChild(h("svg:text", { x: cx, y: cy + 24, "text-anchor": "middle", class: "unit" }, format(v).split(" ")[1] || ""));
  container.appendChild(svg);
  container.appendChild(h("div", { class: "gauge-label" }, h("b", null, label),
    peak ? h("span", { class: "muted" }, `peak ${format(peak.top)}, ${fmtAgo(peak.at / 1000)}`) : null,
    sub ? h("span", { class: "muted" }, sub) : null));
}
