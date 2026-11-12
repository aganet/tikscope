// Hand-rolled SVG charts: stacked columns, multi-series lines and sparklines.
// Marks are thin, grids are hairlines, and every chart carries a hover layer.
import { h, clear } from "./dom.js";
import { fmtNum, tickFormatter, bucketLabel } from "./format.js";

const PAD = { top: 8, right: 8, bottom: 22, left: 48 };
const tooltipEl = () => document.getElementById("tooltip");

function showTooltip(x, y, title, rows) {
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

function niceMax(v) {
  if (v <= 0) return 1;
  const p = Math.pow(10, Math.floor(Math.log10(v)));
  const n = v / p;
  const step = n <= 1 ? 1 : n <= 2 ? 2 : n <= 2.5 ? 2.5 : n <= 5 ? 5 : 10;
  return step * p;
}

function yTicks(max) {
  return [0, 0.25, 0.5, 0.75, 1].map((f) => f * max);
}

function frame(width, height, bucketSecs, times, maxY, yFormat) {
  const svg = h("svg:svg", { viewBox: `0 0 ${width} ${height}`, width, height });
  const plotW = width - PAD.left - PAD.right, plotH = height - PAD.top - PAD.bottom;
  const grid = h("svg:g", { class: "grid" });
  const axis = h("svg:g", { class: "axis" });
  for (const t of yTicks(maxY)) {
    const y = PAD.top + plotH - (t / maxY) * plotH;
    grid.appendChild(h("svg:line", { x1: PAD.left, x2: PAD.left + plotW, y1: y, y2: y }));
    axis.appendChild(h("svg:text", { x: PAD.left - 6, y: y + 4, "text-anchor": "end" }, yFormat(t)));
  }
  const n = times.length;
  const tf = tickFormatter(bucketSecs);
  const step = Math.max(1, Math.ceil(n / Math.max(2, Math.floor(plotW / 90))));
  for (let i = 0; i < n; i += step) {
    const x = PAD.left + ((i + 0.5) / n) * plotW;
    axis.appendChild(h("svg:text", { x, y: height - 6, "text-anchor": "middle" }, tf(times[i])));
  }
  svg.appendChild(grid);
  svg.appendChild(axis);
  return { svg, plotW, plotH };
}

export function legend(series, line = false) {
  return h("div", { class: "legend" }, series.map((s) => h("span", null, h("i", { class: line ? "line" : "", style: { background: s.color } }), s.label)));
}

// Stacked columns. buckets: [{ts, ...}], series: [{key, label, color}].
export function stackedBars(container, { buckets, bucketSecs, series, height = 220, yFormat = fmtNum, valueFormat = fmtNum }) {
  clear(container);
  container.classList.add("chart");
  const width = Math.max(320, container.clientWidth || 600);
  if (!buckets.length) {
    container.appendChild(h("div", { class: "chart-empty" }, "No data in this range"));
    return;
  }
  const totals = buckets.map((b) => series.reduce((s, k) => s + (b[k.key] || 0), 0));
  const maxY = niceMax(Math.max(...totals));
  const { svg, plotW, plotH } = frame(width, height, bucketSecs, buckets.map((b) => b.ts), maxY, yFormat);
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
    ]));
    rect.addEventListener("pointerleave", hideTooltip);
    hover.appendChild(rect);
  });
  svg.appendChild(bars);
  svg.appendChild(hover);
  container.appendChild(svg);
}

// Lines. series: [{label, color, points: [{ts, v}]}], v may be null for gaps.
export function lines(container, { series, bucketSecs, height = 200, yFormat = fmtNum, valueFormat = fmtNum, area = false, maxY: fixedMax }) {
  clear(container);
  container.classList.add("chart");
  const width = Math.max(320, container.clientWidth || 600);
  const times = series[0] ? series[0].points.map((p) => p.ts) : [];
  const all = series.flatMap((s) => s.points.map((p) => p.v).filter((v) => v !== null && v !== undefined));
  if (!times.length || !all.length) {
    container.appendChild(h("div", { class: "chart-empty" }, "No data in this range"));
    return;
  }
  const maxY = fixedMax || niceMax(Math.max(...all));
  const { svg, plotW, plotH } = frame(width, height, bucketSecs, times, maxY, yFormat);
  const n = times.length;
  const xAt = (i) => PAD.left + ((i + 0.5) / n) * plotW;
  const yAt = (v) => PAD.top + plotH - (Math.min(v, maxY) / maxY) * plotH;
  for (const s of series) {
    let d = "", areaD = "", start = null;
    s.points.forEach((p, i) => {
      if (p.v === null || p.v === undefined) {
        if (start !== null) { areaD += ` L${xAt(i - 1)} ${PAD.top + plotH} Z`; start = null; }
        return;
      }
      const x = xAt(i), y = yAt(p.v);
      if (start === null) { d += `M${x} ${y}`; areaD += `M${x} ${PAD.top + plotH} L${x} ${y}`; start = i; }
      else { d += ` L${x} ${y}`; areaD += ` L${x} ${y}`; }
    });
    if (start !== null) areaD += ` L${xAt(s.points.length - 1)} ${PAD.top + plotH} Z`;
    if (area && series.length === 1) svg.appendChild(h("svg:path", { d: areaD, fill: s.color, "fill-opacity": "0.1" }));
    svg.appendChild(h("svg:path", { d, fill: "none", stroke: s.color, "stroke-width": "2", "stroke-linejoin": "round", "stroke-linecap": "round" }));
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
      return { label: s.label, value: v === null || v === undefined ? "-" : valueFormat(v), color: s.color };
    });
    showTooltip(e.clientX, e.clientY, bucketLabel(times[i], bucketSecs), rows);
  });
  hover.addEventListener("pointerleave", () => { cross.style.display = "none"; dots.forEach((d) => (d.style.display = "none")); hideTooltip(); });
  svg.appendChild(hover);
  container.appendChild(svg);
}

export function sparkline(points, color, width = 120, height = 28) {
  const vals = points.map((p) => (p === null || p === undefined ? null : p));
  const present = vals.filter((v) => v !== null);
  const svg = h("svg:svg", { class: "sparkline", viewBox: `0 0 ${width} ${height}`, width, height, "aria-hidden": "true" });
  if (!present.length) return svg;
  const max = Math.max(...present, 1), n = vals.length;
  let d = "", open = false;
  vals.forEach((v, i) => {
    if (v === null) { open = false; return; }
    const x = (i / Math.max(1, n - 1)) * width, y = height - 2 - (v / max) * (height - 4);
    d += `${open ? " L" : " M"}${x.toFixed(1)} ${y.toFixed(1)}`;
    open = true;
  });
  svg.appendChild(h("svg:path", { d, fill: "none", stroke: color, "stroke-width": "1.5", "stroke-linejoin": "round" }));
  return svg;
}

export function bucketsToPoints(buckets, field = "avg") {
  return buckets.map((b) => ({ ts: b.ts, v: b[field] === undefined ? null : b[field] }));
}

// draw runs after the current DOM update has been attached and laid out, so
// charts measure their real container width.
export function draw(fn) {
  requestAnimationFrame(fn);
}
