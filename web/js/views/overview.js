import { draw, lines, legend, stackedBars, sparkline, gauge, peakHold, bucketsToPoints, alignedPoints, niceMax } from "../charts.js";
import { COMPARE, compareRange, compareLabel } from "../range.js";
import { h, icon, card, sectionTitle } from "../dom.js";
import { api } from "../api.js";
import { fmtNum, fmtPct, fmtMs, fmtBps, fmtInt, fmtAgo, outcomes, outcomeTotals } from "../format.js";
import { tile, rankedList, segmentedBar, severityDot } from "../widgets.js";

const seriesColors = ["var(--viz-1)", "var(--viz-2)", "var(--viz-3)", "var(--viz-4)"];

export const overview = {
  async load(state, signal) {
    const cmp = compareRange(state);
    const [dns, net, issues, events, prevDns, prevNet] = await Promise.all([
      api.dnsSummary(state.range, 8, signal),
      api.network(state.range, "", signal),
      api.issues(signal),
      api.events({ range: state.range, severity: "problem" }, 1, 6, signal),
      cmp ? api.dnsSummary(cmp, 1, signal) : null,
      cmp ? api.network(cmp, "", signal) : null,
    ]);
    return { dns, net, issues, events, prevDns, prevNet };
  },

  render({ dns, net, issues, events, prevDns, prevNet }, state, { navigate, openDrawer }) {
    const blocks = [];

    blocks.push(issues.length
      ? h("div", { class: "issues" }, issues.map((i) => h("div", { class: `issue ${i.severity}` },
        h("span", { class: `sev sev-${i.severity}` }),
        h("div", null, h("div", { class: "title" }, i.title), h("div", { class: "detail" }, i.detail)))))
      : allClear());

    if (net.router && net.router.identity) {
      blocks.push(networkNow(net, state, { navigate, openDrawer }, 220, prevNet));
      blocks.push(interfaceCards(net, state, openDrawer));
    } else {
      blocks.push(h("div", { class: "card" }, h("b", null, "Router polling is not configured."), " ",
        h("span", { class: "muted" }, "Set MIKROTIK_URL, MIKROTIK_USER and MIKROTIK_PASSWORD to get throughput speedometers, interface graphs, system load and device bandwidth here.")));
    }
    if (net.probes.length) blocks.push(card(sectionTitle("zap", "sky", "Reachability", "Round trip time from the collector"), probeChart(net)));
    blocks.push(dnsSection(dns, state, { navigate, openDrawer }, prevDns));
    blocks.push(h("div", { class: "grid cols-2" },
      card(sectionTitle("check", "green", "DNS outcomes", "As reported by the router"),
        segmentedBar(outcomes.map((o) => ({ key: o.filter, label: o.label, value: outcomeTotals(dns.totals.outcomes)[o.key], color: o.color, iconName: o.icon })), (k) => navigate("dns", { outcome: k }))),
      card(sectionTitle("list", "rose", "Recent router warnings", `${fmtInt(events.total)} in this range`),
        events.items.length
          ? h("ul", { class: "ranked" }, events.items.map((e) => h("li", null, h("div", { class: "row" },
            h("span", { class: "body" }, h("span", { class: "line" }, h("span", { class: "name" }, severityDot(e.severity), " ", h("span", { class: "mono muted" }, e.topics)), h("span", { class: "muted" }, fmtAgo(e.ts))), h("div", null, e.message))))))
          : h("p", { class: "empty" }, "No warnings or errors logged"),
        h("div", { style: { marginTop: "8px" } }, h("button", { class: "btn small", onclick: () => navigate("events", { severity: "problem" }) }, "All events")))));
    return blocks;
  },
};

// The all-clear banner is dismissable; the choice persists across the 1s
// re-renders and page loads.
const allClearKey = "tikscope.allClearHidden";

function allClear() {
  let hidden = false;
  try { hidden = localStorage.getItem(allClearKey) === "1"; } catch {}
  if (hidden) return null;
  const el = h("div", { class: "all-clear" }, icon("check"),
    h("div", null, h("b", null, "No issues detected"), h("div", { class: "muted" }, "Probes, DNS answers, interfaces and router logs look normal.")),
    h("button", { class: "icon-btn", title: "Hide", "aria-label": "Hide", onclick: () => { try { localStorage.setItem(allClearKey, "1"); } catch {} el.remove(); } }, icon("x")));
  return el;
}

// The WAN interface's live rate from the latest poll, falling back to the
// last timeline bucket.
function current(net, dir) {
  const wan = net.interfaces.find((i) => i.wan) || net.interfaces.find((i) => i.name === net.wan);
  if (wan) return dir === "rx" ? wan.rx_bps : wan.tx_bps;
  const b = net.throughput[dir] || [];
  for (let i = b.length - 1; i >= 0; i--) if (b[i].avg !== null) return b[i].avg;
  return null;
}

function avgPeak(buckets) {
  return Math.max(0, ...(buckets || []).map((b) => b.avg || 0));
}

export function networkNow(net, state, { navigate, openDrawer }, chartHeight = 220, prevNet = null) {
  const rx = net.throughput.rx || [], tx = net.throughput.tx || [];
  const down = current(net, "rx"), up = current(net, "tx");
  // Without a line speed the arc scales like the chart, to the busiest bucket
  // average, so one burst does not shrink the needle for the next day.
  const downMax = net.limits.down || niceMax(Math.max(avgPeak(rx), down || 0, 1e6));
  const upMax = net.limits.up || niceMax(Math.max(avgPeak(tx), up || 0, 1e6));
  const gDown = h("div"), gUp = h("div");
  draw(() => {
    gauge(gDown, { value: down, max: downMax, label: "Download", color: "var(--viz-1)", format: fmtBps, peak: peakHold(`${net.wan}:rx`, down) });
    gauge(gUp, { value: up, max: upMax, label: "Upload", color: "var(--viz-2)", format: fmtBps, peak: peakHold(`${net.wan}:tx`, up) });
  });
  const probe = net.probes[0];
  const r = net.router;
  const stats = h("div", { class: "stat-col" },
    probe ? tile({ label: "Latency", value: fmtMs(probe.last_rtt_ms || probe.rtt_avg_ms), detail: probe.loss_pct !== null ? `${probe.target.addr}, ${probe.loss_pct.toFixed(1)}% loss` : probe.target.addr, iconName: "zap", tone: probe.up ? "sky" : "rose", spark: sparkline(probe.rtt.map((b) => b.avg), "var(--viz-1)"), onClick: () => navigate("network") }) : null,
    tile({ label: "Router CPU", value: `${r.cpu_load}%`, detail: r.total_memory ? `${fmtPct(r.total_memory - r.free_memory, r.total_memory, 0)} memory used` : "", iconName: "cpu", tone: "violet", spark: sparkline((net.system.cpu || []).map((b) => b.avg), "var(--viz-1)"), onClick: () => navigate("system") }),
    tile({ label: "Connections", value: fmtNum(r.connections), detail: r.max_connections ? `of ${fmtNum(r.max_connections)} tracked` : "Tracked by the firewall", iconName: "link", tone: "blue", spark: sparkline((net.system.connections || []).map((b) => b.avg), "var(--viz-1)"), onClick: () => navigate("network") }));
  const chart = h("div", { class: "fill", style: { marginTop: "16px" } });
  draw(() => {
    const series = throughputSeries(net, state, prevNet);
    lines(chart, { series, bucketSecs: net.bucket_secs, height: chartHeight, yFormat: (v) => fmtBps(v), valueFormat: fmtBps, band: true, scale: chartScale() });
    chart.appendChild(legend(series, true));
  });
  return h("div", { class: "card" },
    h("div", { class: "card-head" }, sectionTitle("download", "green", `${r.identity} on ${net.wan || "WAN"}`, `Live rate from the last poll, ${bucketText(net.bucket_secs)} averages and peaks on the chart`),
      h("div", { class: "head-tools" }, scaleToggle(), h("button", { class: "btn small", onclick: () => navigate("network") }, "Network details"))),
    h("div", { class: "gauges" }, gDown, gUp, stats),
    chart);
}

export function interfaceCards(net, state, openDrawer, max = 8, chartHeight = 110) {
  const shown = net.interfaces.filter((i) => !i.disabled && i.name !== "lo" && (i.running || i.rx_bps > 0 || i.tx_bps > 0)).slice(0, max);
  if (!shown.length) return null;
  return card(sectionTitle("router", "violet", "Interfaces", `${shown.length} active of ${net.interfaces.length}`),
    h("div", { class: "ifaces" }, shown.map((i) => {
      const el = h("div", { class: "fill" });
      const data = net.all[i.name] || {};
      draw(() => lines(el, { series: [
        { label: "Download", color: "var(--viz-1)", points: bucketsToPoints(data.rx || []) },
        { label: "Upload", color: "var(--viz-2)", points: bucketsToPoints(data.tx || []) },
      ], bucketSecs: net.bucket_secs, height: chartHeight, yFormat: (v) => fmtBps(v), valueFormat: fmtBps }));
      return h("div", { class: "iface-card", role: "button", tabindex: 0, onclick: () => openDrawer({ kind: "interface", key: i.name }, state),
        onkeydown: (e) => { if (e.key === "Enter") openDrawer({ kind: "interface", key: i.name }, state); } },
        h("div", { class: "head" },
          h("b", null, h("span", { class: "status-pill" }, h("span", { class: "dot", style: { background: i.running ? "var(--good)" : "var(--critical)" } })), i.name, i.wan ? h("span", { class: "badge" }, "WAN") : null),
          h("span", { class: "muted" }, i.comment || i.type)),
        h("div", { class: "nums" }, h("span", null, "down ", h("b", null, fmtBps(i.rx_bps))), h("span", null, "up ", h("b", null, fmtBps(i.tx_bps))), i.errors ? h("span", null, "errors ", h("b", null, fmtInt(i.errors))) : null),
        el);
    })));
}

// The y axis of the throughput charts: auto, linear or log, remembered.
export function chartScale() {
  try { return localStorage.getItem("tikscope.scale") || "auto"; } catch { return "auto"; }
}

export function scaleToggle() {
  const cur = chartScale();
  const btn = (v, label, title) => h("button", { type: "button", class: cur === v ? "active" : "", title, onclick: () => {
    try { localStorage.setItem("tikscope.scale", v); } catch {}
    document.getElementById("refresh").click();
  } }, label);
  return h("div", { class: "seg-group", role: "group", "aria-label": "Y axis scale" },
    btn("auto", "Auto", "Log when bursts dwarf the usual traffic"), btn("linear", "Linear", "Linear y axis"), btn("log", "Log", "Logarithmic y axis"));
}

// The comparison line is only the download: two more dashed lines would
// bury the live ones.
function throughputSeries(net, state, prevNet) {
  const rx = net.throughput.rx || [], tx = net.throughput.tx || [];
  const series = [
    { label: "Download", color: "var(--viz-1)", points: bucketsToPoints(rx) },
    { label: "Upload", color: "var(--viz-2)", points: bucketsToPoints(tx) },
  ];
  if (prevNet && COMPARE[state.compare]) {
    series.push({ label: `Download ${compareLabel(state)}`, color: "var(--viz-1)", dash: true, points: alignedPoints(rx, prevNet.throughput.rx, COMPARE[state.compare].secs) });
  }
  return series;
}

function dnsSection(dns, state, { navigate, openDrawer }, prevDns = null) {
  const t = dns.totals;
  const byOutcome = outcomeTotals(t.outcomes);
  const problems = byOutcome.nxdomain + byOutcome.blocked + byOutcome.failed;
  const minutes = Math.max(1, (dns.until - dns.since) / 60);
  const chart = h("div");
  draw(() => {
    const present = outcomes.filter((o) => dns.timeline.some((b) => b[o.key] > 0));
    const series = present.length ? present : [outcomes[0]];
    stackedBars(chart, { buckets: dns.timeline, bucketSecs: dns.bucket_secs, series, height: 180, overlay: dnsOverlay(dns, state, prevDns) });
    if (series.length > 1) chart.appendChild(legend(series));
  });
  return h("div", { class: "grid", style: { gap: "12px" } },
    h("div", { class: "tiles" },
      tile({ label: "DNS queries", value: fmtNum(t.queries), detail: `${(t.queries / minutes).toFixed(1)} per minute`, iconName: "activity", tone: "blue", onClick: () => navigate("dns") }),
      tile({ label: "DNS problems", value: fmtNum(problems), detail: `${fmtPct(problems, t.queries)} of queries`, iconName: "alert", tone: "amber", onClick: () => navigate("dns", { outcome: "problem" }) }),
      tile({ label: "Blocked", value: fmtNum(byOutcome.blocked), detail: "Adlist or static block answers", iconName: "ban", tone: "rose", onClick: () => navigate("dns", { outcome: "blocked" }) }),
      tile({ label: "DNS response", value: fmtMs(t.latency_ms), detail: t.latency_ms === null ? "Needs RouterOS 7 answers" : "Average, query to answer", iconName: "timer", tone: "sky" }),
      tile({ label: "Clients", value: fmtNum(t.clients), detail: `${fmtNum(t.domains)} domains`, iconName: "users", tone: "green", onClick: () => navigate("devices") })),
    h("div", { class: "card" }, h("div", { class: "card-head" }, sectionTitle("activity", "blue", "DNS queries over time", `Stacked by outcome, ${bucketText(dns.bucket_secs)} buckets`), h("button", { class: "btn small", onclick: () => navigate("dns") }, "DNS details")), chart),
    h("div", { class: "grid cols-3" },
      card(sectionTitle("globe", "blue", "Top domains", "Most requested names"),
        rankedList(dns.top_domains.map((d) => ({ key: d.domain, label: d.domain, title: d.domain, value: d.count, sub: `${d.clients} client${d.clients === 1 ? "" : "s"}` })), (k) => openDrawer({ kind: "domain", key: k }, state))),
      card(sectionTitle("users", "green", "Top clients", "Most active devices"),
        rankedList(dns.top_clients.map((c) => ({ key: c.client, label: c.name ? [c.name, h("span", { class: "mono" }, c.client)] : h("span", { class: "mono" }, c.client), title: c.client, value: c.count, sub: `${c.domains} domain${c.domains === 1 ? "" : "s"}` })), (k) => openDrawer({ kind: "client", key: k }, state))),
      card(sectionTitle("alert", "amber", "Problem domains", "Blocked, NXDOMAIN or failed"),
        rankedList(dns.problems.map((d) => ({ key: d.domain, label: d.domain, title: d.domain, value: d.problems, sub: `${d.clients} client${d.clients === 1 ? "" : "s"}` })), (k) => openDrawer({ kind: "domain", key: k }, state), "No problems in this range"))));
}

export function bucketText(secs) {
  return secs >= 3600 ? `${secs / 3600}h` : secs >= 60 ? `${secs / 60}min` : `${secs}s`;
}

// Total queries of the earlier window as a dashed line over the outcome bars.
export function dnsOverlay(dns, state, prevDns) {
  if (!prevDns || !COMPARE[state.compare]) return null;
  return { label: `Queries ${compareLabel(state)}`, color: "var(--text-2)", points: alignedPoints(dns.timeline, prevDns.timeline, COMPARE[state.compare].secs, "total") };
}

export function throughputChart(net, state, height = 200, prevNet = null) {
  const el = h("div");
  draw(() => {
    const series = throughputSeries(net, state, prevNet);
    lines(el, { series, bucketSecs: net.bucket_secs, height, yFormat: (v) => fmtBps(v), valueFormat: fmtBps, band: true, scale: chartScale() });
    el.appendChild(legend(series, true));
  });
  return el;
}

export function probeChart(net, height = 160) {
  const el = h("div", { class: "fill" });
  draw(() => {
    const series = net.probes.map((p, i) => ({ label: `${p.target.kind} ${p.target.addr}`, color: seriesColors[i % seriesColors.length], points: bucketsToPoints(p.rtt) }));
    lines(el, { series, bucketSecs: net.bucket_secs, height, yFormat: (v) => fmtMs(v), valueFormat: fmtMs });
    el.appendChild(legend(series, true));
  });
  return el;
}
