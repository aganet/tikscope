import { draw } from "../charts.js";
import { h, icon, card, sectionTitle } from "../dom.js";
import { api } from "../api.js";
import { fmtNum, fmtPct, fmtMs, fmtInt, outcomes, outcomeTotals } from "../format.js";
import { stackedBars, lines, legend, bucketsToPoints } from "../charts.js";
import { tile, rankedList, segmentedBar, queriesTable, pager } from "../widgets.js";
import { bucketText, dnsOverlay } from "./overview.js";
import { compareRange } from "../range.js";

const outcomeOptions = [["", "Any outcome"], ["resolved", "Resolved"], ["problem", "Any problem"], ["nxdomain", "NXDOMAIN"], ["blocked", "Blocked"], ["failed", "Failed"], ["nodata", "No data"], ["unknown", "No reply"]];

function filterState(state) {
  state.views.dns ||= { search: "", client: "", type: "", outcome: "", page: 1, pageSize: 50 };
  // A hash parameter pre-fills the outcome once, e.g. from the overview tiles.
  const params = new URLSearchParams((location.hash.split("?")[1]) || "");
  if (params.has("outcome")) { state.views.dns.outcome = params.get("outcome"); state.views.dns.page = 1; history.replaceState(null, "", "#dns"); }
  return state.views.dns;
}

export const dns = {
  async load(state, signal) {
    const f = filterState(state);
    const apiFilter = { range: state.range, search: f.search, client: f.client, type: f.type.toUpperCase(), outcome: f.outcome };
    const cmp = compareRange(state);
    const [summary, log, prevSummary] = await Promise.all([
      api.dnsSummary(state.range, 10, signal),
      api.dnsQueries(apiFilter, f.page, f.pageSize, signal),
      cmp ? api.dnsSummary(cmp, 1, signal) : null,
    ]);
    return { summary, log, apiFilter, prevSummary };
  },

  render({ summary, log, apiFilter, prevSummary }, state, { openDrawer }) {
    const f = filterState(state);
    const t = summary.totals;
    const byOutcome = outcomeTotals(t.outcomes);
    const problems = byOutcome.nxdomain + byOutcome.blocked + byOutcome.failed;
    const rerender = () => { f.page = 1; document.getElementById("refresh").click(); };
    let debounce;
    const onInput = (key) => (e) => { f[key] = e.target.value; clearTimeout(debounce); debounce = setTimeout(rerender, 350); };

    const tiles = h("div", { class: "tiles" },
      tile({ label: "Queries", value: fmtNum(t.queries), detail: `${fmtNum(t.domains)} domains, ${fmtNum(t.clients)} clients`, iconName: "activity", tone: "blue" }),
      tile({ label: "Problems", value: fmtNum(problems), detail: `${fmtPct(problems, t.queries)} of queries`, iconName: "alert", tone: "amber" }),
      tile({ label: "Blocked", value: fmtNum(byOutcome.blocked), detail: "Adlist or static block answers", iconName: "ban", tone: "rose" }),
      tile({ label: "Response time", value: fmtMs(t.latency_ms), detail: "Average, query to answer", iconName: "timer", tone: "sky" }),
      tile({ label: "IPv6 clients", value: fmtPct(t.ipv6, t.queries, 0), detail: "Share of queries over IPv6", iconName: "globe", tone: "violet" }));

    const timelineEl = h("div"), latencyEl = h("div");
    draw(() => {
      const present = outcomes.filter((o) => summary.timeline.some((b) => b[o.key] > 0));
      const series = present.length ? present : [outcomes[0]];
      stackedBars(timelineEl, { buckets: summary.timeline, bucketSecs: summary.bucket_secs, series, height: 220, overlay: dnsOverlay(summary, state, prevSummary) });
      if (series.length > 1) timelineEl.appendChild(legend(series));
      const hasLatency = summary.timeline.some((b) => b.latency_ms !== null);
      if (hasLatency) lines(latencyEl, { series: [{ label: "Avg response", color: "var(--viz-1)", points: bucketsToPoints(summary.timeline, "latency_ms") }], bucketSecs: summary.bucket_secs, height: 160, yFormat: (v) => fmtMs(v), valueFormat: fmtMs, area: true });
      else latencyEl.appendChild(h("p", { class: "empty" }, "No answer lines received yet. RouterOS 7 logs them with the dns topic."));
    });

    const charts = h("div", { class: "grid cols-3" },
      h("div", { class: "card", style: { gridColumn: "span 2" } }, h("div", { class: "card-head" }, sectionTitle("activity", "blue", "Queries over time", `Stacked by outcome, ${bucketText(summary.bucket_secs)} buckets`)), timelineEl),
      card(sectionTitle("timer", "sky", "Response time", "Query to answer"), latencyEl));

    const breakdown = h("div", { class: "grid cols-3" },
      card(sectionTitle("check", "green", "Outcomes", "As reported by the router"),
        segmentedBar(outcomes.map((o) => ({ key: o.filter, label: o.label, value: byOutcome[o.key], color: o.color, iconName: o.icon })), (k) => { f.outcome = k; rerender(); })),
      card(sectionTitle("list", "violet", "Record types", `${summary.types.length} types seen`),
        rankedList(summary.types.slice(0, 8).map((x) => ({ key: x.key, label: h("span", { class: "mono" }, x.key), value: x.count, sub: fmtPct(x.count, t.queries) })), (k) => { f.type = k; rerender(); })),
      card(sectionTitle("alert", "amber", "Problem domains", "Blocked, NXDOMAIN or failed"),
        rankedList(summary.problems.map((d) => ({ key: d.domain, label: d.domain, title: d.domain, value: d.problems, sub: `${d.clients} client${d.clients === 1 ? "" : "s"}` })), (k) => openDrawer({ kind: "domain", key: k }, state), "No problems in this range")));

    const top = h("div", { class: "grid cols-2" },
      card(sectionTitle("globe", "blue", "Top domains", "Most requested names"),
        rankedList(summary.top_domains.map((d) => ({ key: d.domain, label: d.domain, title: d.domain, value: d.count, sub: `${d.clients} client${d.clients === 1 ? "" : "s"}` })), (k) => openDrawer({ kind: "domain", key: k }, state))),
      card(sectionTitle("users", "green", "Top clients", "Most active devices"),
        rankedList(summary.top_clients.map((c) => ({ key: c.client, label: c.name ? [c.name, h("span", { class: "mono" }, c.client)] : h("span", { class: "mono" }, c.client), title: c.client, value: c.count, sub: `${c.domains} domain${c.domains === 1 ? "" : "s"}` })), (k) => openDrawer({ kind: "client", key: k }, state))));

    const filtered = f.search || f.client || f.type || f.outcome;
    const filters = h("div", { class: "filters" },
      h("input", { type: "search", placeholder: "Domain contains", value: f.search, oninput: onInput("search") }),
      h("input", { type: "text", class: "mono", placeholder: "Client address", value: f.client, oninput: onInput("client"), style: { width: "170px" } }),
      h("input", { type: "text", class: "mono", placeholder: "Type", value: f.type, oninput: onInput("type"), style: { width: "90px", minWidth: "0" } }),
      h("select", { onchange: (e) => { f.outcome = e.target.value; rerender(); } }, outcomeOptions.map(([v, l]) => h("option", { value: v, selected: v === f.outcome }, l))),
      filtered ? h("button", { class: "btn", onclick: () => { Object.assign(f, { search: "", client: "", type: "", outcome: "" }); rerender(); } }, icon("x"), "Clear") : null,
      h("span", { class: "spacer" }),
      h("select", { onchange: (e) => { f.pageSize = Number(e.target.value); rerender(); } }, [25, 50, 100, 250].map((n) => h("option", { value: n, selected: n === f.pageSize }, `${n} rows`))),
      h("a", { class: "btn", href: api.dnsExportUrl(apiFilter), download: "" }, icon("download"), "Export CSV"));

    const logCard = card(sectionTitle("list", "violet", "Query log", `${fmtInt(log.total)} queries match`),
      filters,
      queriesTable(log.items, { onClient: (c) => openDrawer({ kind: "client", key: c }, state), onDomain: (d) => openDrawer({ kind: "domain", key: d }, state) }),
      pager({ page: log.page, total: log.total, pageSize: log.page_size, onPage: (p) => { f.page = p; document.getElementById("refresh").click(); }, label: "queries" }));

    return [tiles, charts, breakdown, top, logCard];
  },
};
