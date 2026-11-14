// Detail drawer for a client, a domain or an interface.
import { draw } from "./charts.js";
import { h, icon, replace, copy, kv } from "./dom.js";
import { api } from "./api.js";
import { fmtInt, fmtPct, fmtMs, fmtBps, fmtBytes, outcomes, outcomeLabel } from "./format.js";
import { stackedBars, lines, legend, bucketsToPoints, hideTooltip } from "./charts.js";
import { rankedList, queriesTable, pager } from "./widgets.js";
import { wifiBars } from "./views/devices.js";
import { replaceLocation, pushLocation } from "./app.js";

const el = () => document.getElementById("drawer");
const body = () => document.getElementById("drawer-body");
let selection = null;
let ctrl = null;
let page = 1;
// Opening pushes a history entry, so the browser's back closes the drawer.
let pushed = false;

export function drawerOpen() { return selection !== null; }

const currentTab = () => location.hash.split("?")[0].slice(1) || "overview";

// {fromHistory: true} means the URL already changed: back, forward or a link.
export function closeDrawer(opts = {}) {
  if (!selection) return;
  if (!opts.fromHistory && pushed) {
    history.back();
    return;
  }
  selection = null;
  pushed = false;
  if (ctrl) ctrl.abort();
  el().hidden = true;
  hideTooltip();
  if (!opts.fromHistory) replaceLocation(currentTab());
}

// openDrawer(null, state, {refresh:true}) redraws the current selection.
export async function openDrawer(sel, state, opts = {}) {
  if (sel && selection && sel.kind === selection.kind && sel.key === selection.key && !opts.refresh) return;
  if (sel && (!selection || sel.kind !== selection.kind || sel.key !== selection.key)) page = 1;
  const wasOpen = selection !== null;
  if (sel) selection = sel;
  if (!selection) return;
  if (ctrl) ctrl.abort();
  ctrl = new AbortController();
  const signal = ctrl.signal;
  el().hidden = false;
  if (!opts.refresh) {
    replace(body(), h("h2", { class: "mono" }, selection.key), h("p", { class: "sub" }, "Loading"));
    const params = { [selection.kind]: selection.key };
    if (opts.fromHistory) pushed = false;
    else if (wasOpen) replaceLocation(currentTab(), params);
    else { pushLocation(currentTab(), params); pushed = true; }
  }
  try {
    const render = { client: clientDetail, domain: domainDetail, interface: interfaceDetail }[selection.kind];
    const nodes = await render(selection.key, state, signal);
    if (signal.aborted) return;
    replace(body(), nodes);
  } catch (err) {
    if (!signal.aborted) replace(body(), h("h2", { class: "mono" }, selection.key), h("p", { class: "error-banner" }, err.message));
  }
}

function title(key, sub) {
  return [h("h2", { class: "mono" }, key, h("button", { class: "icon-btn", style: { width: "28px", height: "28px" }, title: "Copy", onclick: () => copy(key) }, icon("copy"))), h("p", { class: "sub" }, sub)];
}

function stats(items) {
  return h("div", { class: "stats" }, items.map(([l, v]) => h("div", null, h("div", { class: "l" }, l), h("div", { class: "v num" }, v))));
}

function timelineSection(tl, heading = "Queries over time") {
  const chart = h("div");
  draw(() => {
    const present = outcomes.filter((o) => tl.buckets.some((b) => b[o.key] > 0));
    const series = present.length ? present : [outcomes[0]];
    stackedBars(chart, { buckets: tl.buckets, bucketSecs: tl.bucket_secs, series, height: 170 });
    if (series.length > 1) chart.appendChild(legend(series));
  });
  return h("section", null, h("h3", null, heading), chart);
}

function logSection(log, filter, state, hide) {
  const refresh = (p) => { page = p; openDrawer(null, state, { refresh: true }); };
  return h("section", null,
    h("h3", null, h("span", null, `Queries (${fmtInt(log.total)})`),
      h("a", { class: "btn small", href: `#dns`, onclick: (e) => { e.preventDefault(); state.views.dns = { search: filter.domain || "", client: filter.client || "", type: "", outcome: "", page: 1, pageSize: 50 }; closeDrawer(); location.hash = "#dns"; } }, icon("external"), "Open in query log")),
    queriesTable(log.items, { ...hide, onClient: (c) => openDrawer({ kind: "client", key: c }, state), onDomain: (d) => openDrawer({ kind: "domain", key: d }, state) }),
    log.total > log.page_size ? pager({ page: log.page, total: log.total, pageSize: log.page_size, onPage: refresh, label: "queries" }) : null);
}

async function clientDetail(ip, state, signal) {
  const filter = { range: state.range, client: ip };
  const [tl, head, domains, problems, log, devs, rx, tx] = await Promise.all([
    api.dnsTimeline(filter, signal),
    api.dnsClients({ range: state.range, client: ip }, 1, signal),
    api.dnsDomains(filter, 15, signal),
    api.dnsDomains({ ...filter, outcome: "problem" }, 10, signal),
    api.dnsQueries(filter, page, 25, signal),
    api.devices(state.range, signal),
    api.metrics("dev.rx_bps", ip, state.range, signal),
    api.metrics("dev.tx_bps", ip, state.range, signal),
  ]);
  const info = head[0];
  const dev = devs.find((d) => d.ip === ip);
  const totals = tl.buckets.reduce((a, b) => { a.total += b.total; a.resolved += b.resolved; return a; }, { total: 0, resolved: 0 });
  const bw = rx.series.length || tx.series.length ? (() => {
    const chart = h("div");
    draw(() => {
      const series = [
        { label: "Download", color: "var(--viz-1)", points: bucketsToPoints(rx.series[0] ? rx.series[0].buckets : []) },
        { label: "Upload", color: "var(--viz-2)", points: bucketsToPoints(tx.series[0] ? tx.series[0].buckets : []) },
      ].filter((s) => s.points.length);
      lines(chart, { series, bucketSecs: rx.bucket_secs, height: 170, yFormat: (v) => fmtBps(v), valueFormat: fmtBps, band: true });
      chart.appendChild(legend(series, true));
    });
    return h("section", null, h("h3", null, "Bandwidth"), chart);
  })() : null;

  return [
    ...title(ip, [dev && dev.name ? dev.name : info && info.name ? info.name : "Unnamed device",
      (dev && dev.mac) || (info && info.mac) ? h("span", { class: "mono muted" }, `  ${(dev && dev.mac) || info.mac}`) : null,
      dev && dev.wifi ? h("span", { class: "muted" }, "  ", wifiBars(dev.wifi.signal_dbm), `${dev.wifi.signal_dbm} dBm on ${dev.wifi.interface}`) : null]),
    stats([
      ["Queries", info ? fmtInt(info.count) : "0"],
      ["Domains", info ? fmtInt(info.domains) : "0"],
      ["Problems", info ? `${fmtInt(info.problems)} (${fmtPct(info.problems, info.count, 0)})` : "0"],
      ["Resolved", fmtPct(totals.resolved, totals.total, 0)],
      dev && dev.rx_bps !== null ? ["Download now", fmtBps(dev.rx_bps)] : null,
      dev && dev.tx_bps !== null ? ["Upload now", fmtBps(dev.tx_bps)] : null,
    ].filter(Boolean)),
    timelineSection(tl),
    bw,
    h("div", { class: "grid cols-2" },
      h("section", null, h("h3", null, "Top domains"), rankedList(domains.map((d) => ({ key: d.domain, label: d.domain, title: d.domain, value: d.count })), (k) => openDrawer({ kind: "domain", key: k }, state))),
      h("section", null, h("h3", null, "Problem domains"), rankedList(problems.map((d) => ({ key: d.domain, label: d.domain, title: d.domain, value: d.problems })), (k) => openDrawer({ kind: "domain", key: k }, state), "No problems in this range"))),
    logSection(log, { client: ip }, state, { hideClient: true }),
  ];
}

async function domainDetail(domain, state, signal) {
  const filter = { range: state.range, domain };
  const [tl, head, clients, log] = await Promise.all([
    api.dnsTimeline(filter, signal),
    api.dnsDomains(filter, 1, signal),
    api.dnsClients(filter, 15, signal),
    api.dnsQueries(filter, page, 25, signal),
  ]);
  const info = head[0];
  const totals = tl.buckets.reduce((a, b) => { a.total += b.total; a.resolved += b.resolved; return a; }, { total: 0, resolved: 0 });
  return [
    ...title(domain, "Domain activity in the selected range"),
    stats([
      ["Queries", info ? fmtInt(info.count) : "0"],
      ["Clients", info ? fmtInt(info.clients) : "0"],
      ["Problems", info ? `${fmtInt(info.problems)} (${fmtPct(info.problems, info.count, 0)})` : "0"],
      ["Resolved", fmtPct(totals.resolved, totals.total, 0)],
    ]),
    timelineSection(tl),
    resolveSection(domain),
    h("section", null, h("h3", null, "Clients"), rankedList(clients.map((c) => ({ key: c.client, label: c.name ? [c.name, h("span", { class: "mono" }, c.client)] : h("span", { class: "mono" }, c.client), title: c.client, value: c.count })), (k) => openDrawer({ kind: "client", key: k }, state))),
    logSection(log, { domain }, state, { hideDomain: true }),
  ];
}

// Looks the domain up right now through the collector's resolver so the
// logged answers can be compared with the current state.
function resolveSection(domain) {
  const result = h("div", { class: "resolve-result" });
  const run = async (type) => {
    replace(result, h("span", { class: "muted" }, "Resolving"));
    try {
      const r = await api.dnsResolve(domain, type);
      replace(result, h("b", null, outcomeLabel[r.outcome] || r.outcome), h("span", { class: "muted" }, ` ${fmtMs(r.duration_ms)}`),
        r.error ? h("div", { class: "muted mono" }, r.error) : null,
        r.records.length ? h("ul", { class: "mono" }, r.records.map((x) => h("li", null, x))) : null);
    } catch (err) {
      replace(result, h("span", { class: "muted" }, err.message));
    }
  };
  return h("section", null, h("h3", null, "Resolve now"),
    h("div", { class: "inline-actions" }, ["A", "AAAA", "CNAME", "MX", "TXT"].map((t) => h("button", { class: "btn small mono", onclick: () => run(t) }, t))),
    result);
}

async function interfaceDetail(name, state, signal) {
  const [net, errs, drops, downs] = await Promise.all([
    api.network(state.range, name, signal),
    api.metrics("iface.errors", name, state.range, signal),
    api.metrics("iface.drops", name, state.range, signal),
    api.metrics("iface.link_downs", name, state.range, signal),
  ]);
  const iface = net.interfaces.find((i) => i.name === name) || {};
  const chart = h("div");
  draw(() => {
    const series = [
      { label: "Download", color: "var(--viz-1)", points: bucketsToPoints(net.throughput.rx || []) },
      { label: "Upload", color: "var(--viz-2)", points: bucketsToPoints(net.throughput.tx || []) },
    ];
    lines(chart, { series, bucketSecs: net.bucket_secs, height: 200, yFormat: (v) => fmtBps(v), valueFormat: fmtBps, band: true });
    chart.appendChild(legend(series, true));
  });
  const sum = (m) => (m.series[0] ? m.series[0].buckets.reduce((a, b) => a + (b.avg || 0) * b.n, 0) : 0);
  const peak = (b) => Math.max(0, ...(b || []).map((x) => x.max || 0));
  return [
    ...title(name, [iface.type || "interface", iface.link_rate ? ` ${iface.link_rate}${iface.duplex ? " " + iface.duplex + " duplex" : ""}` : "", iface.comment ? `, ${iface.comment}` : "", iface.wan ? " (WAN)" : ""]),
    stats([
      ["State", iface.disabled ? "disabled" : iface.running ? "running" : "down"],
      ["Peak download", fmtBps(peak(net.throughput.rx))],
      ["Peak upload", fmtBps(peak(net.throughput.tx))],
      ["Received total", fmtBytes(iface.rx_bytes || 0)],
      ["Sent total", fmtBytes(iface.tx_bytes || 0)],
      ["Errors in range", fmtInt(sum(errs))],
      ["Drops in range", fmtInt(sum(drops))],
      ["Link downs in range", fmtInt(sum(downs))],
    ]),
    h("section", null, h("h3", null, "Throughput"), chart),
    h("section", null, h("h3", null, "Details"), kv([["MAC", iface.mac || "-"], ["Last link up", iface.last_link_up || "-"], ["Lifetime link downs", fmtInt(iface.link_downs || 0)], ["Lifetime errors", fmtInt(iface.errors || 0)]])),
  ];
}
