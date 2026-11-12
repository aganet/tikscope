import { draw } from "../charts.js";
import { h, icon, card, sectionTitle } from "../dom.js";
import { api } from "../api.js";
import { fmtNum, fmtPct, fmtMs, fmtBps, fmtInt, fmtAgo, outcomes, outcomeTotals } from "../format.js";
import { stackedBars, lines, legend, sparkline, bucketsToPoints } from "../charts.js";
import { tile, rankedList, segmentedBar, severityDot } from "../widgets.js";

const seriesColors = ["var(--viz-1)", "var(--viz-2)", "var(--viz-3)", "var(--viz-4)"];

export const overview = {
  async load(state, signal) {
    const [dns, net, issues, events] = await Promise.all([
      api.dnsSummary(state.range, 8, signal),
      api.network(state.range, "", signal),
      api.issues(signal),
      api.events({ range: state.range, severity: "problem" }, 1, 6, signal),
    ]);
    return { dns, net, issues, events };
  },

  render({ dns, net, issues, events }, state, { navigate, openDrawer }) {
    const t = dns.totals;
    const byOutcome = outcomeTotals(t.outcomes);
    const problems = byOutcome.nxdomain + byOutcome.blocked + byOutcome.failed;
    const minutes = Math.max(1, (dns.until - dns.since) / 60);
    const rx = net.throughput.rx || [], tx = net.throughput.tx || [];
    const last = (b) => { for (let i = b.length - 1; i >= 0; i--) if (b[i].avg !== null) return b[i].avg; return null; };
    const probe = net.probes[0];

    const tiles = h("div", { class: "tiles" },
      tile({ label: "DNS queries", value: fmtNum(t.queries), detail: `${(t.queries / minutes).toFixed(1)} per minute`, iconName: "activity", tone: "blue", onClick: () => navigate("dns") }),
      tile({ label: "DNS problems", value: fmtNum(problems), detail: `${fmtPct(problems, t.queries)} of queries`, iconName: "alert", tone: "amber", onClick: () => navigate("dns", { outcome: "problem" }) }),
      tile({ label: "DNS response", value: fmtMs(t.latency_ms), detail: t.latency_ms === null ? "Needs RouterOS 7 answers" : "Average, query to answer", iconName: "timer", tone: "sky" }),
      net.wan ? tile({ label: "Download", value: fmtBps(last(rx)), detail: `${net.wan}, current rate`, iconName: "download", tone: "green", spark: sparkline(rx.map((b) => b.avg), "var(--viz-1)"), onClick: () => navigate("network") }) : null,
      net.wan ? tile({ label: "Upload", value: fmtBps(last(tx)), detail: `${net.wan}, current rate`, iconName: "upload", tone: "violet", spark: sparkline(tx.map((b) => b.avg), "var(--viz-2)"), onClick: () => navigate("network") }) : null,
      probe ? tile({ label: "Latency", value: fmtMs(probe.rtt_avg_ms), detail: probe.loss_pct !== null ? `${probe.target.addr}, ${probe.loss_pct.toFixed(1)}% loss` : `${probe.target.addr}, no probes yet`, iconName: "zap", tone: probe.up ? "sky" : "rose", spark: sparkline(probe.rtt.map((b) => b.avg), "var(--viz-1)"), onClick: () => navigate("network") }) : null,
      net.router && net.router.identity ? tile({ label: "Router CPU", value: `${net.router.cpu_load}%`, detail: net.router.total_memory ? `${fmtPct(net.router.total_memory - net.router.free_memory, net.router.total_memory, 0)} memory used` : "", iconName: "cpu", tone: "violet", spark: sparkline((net.system.cpu || []).map((b) => b.avg), "var(--viz-1)"), onClick: () => navigate("system") }) : null,
    );

    const issueList = issues.length
      ? h("div", { class: "issues" }, issues.map((i) => h("div", { class: `issue ${i.severity}` },
        h("span", { class: `sev sev-${i.severity}` }),
        h("div", null, h("div", { class: "title" }, i.title), h("div", { class: "detail" }, i.detail)))))
      : h("div", { class: "all-clear" }, icon("check"), h("div", null, h("b", null, "No issues detected"), h("div", { class: "muted" }, "Probes, DNS answers, interfaces and router logs look normal.")));

    const dnsChart = h("div");
    const chartRow = h("div", { class: `grid ${net.wan ? "cols-2" : ""}` },
      card(sectionTitle("activity", "blue", "DNS queries over time", `Stacked by outcome, ${bucketText(dns.bucket_secs)} buckets`), dnsChart),
      net.wan ? card(sectionTitle("download", "green", `Throughput on ${net.wan}`, "Download and upload"), throughputChart(net, state)) : null);
    draw(() => {
      const present = outcomes.filter((o) => dns.timeline.some((b) => b[o.key] > 0));
      const series = present.length ? present : [outcomes[0]];
      stackedBars(dnsChart, { buckets: dns.timeline, bucketSecs: dns.bucket_secs, series, height: 200 });
      if (series.length > 1) dnsChart.appendChild(legend(series));
    });

    const probeCard = net.probes.length ? card(sectionTitle("zap", "sky", "Reachability", "Round trip time from the collector"), probeChart(net)) : null;

    const lists = h("div", { class: "grid cols-3" },
      card(sectionTitle("globe", "blue", "Top domains", "Most requested names"),
        rankedList(dns.top_domains.map((d) => ({ key: d.domain, label: d.domain, title: d.domain, value: d.count, sub: `${d.clients} client${d.clients === 1 ? "" : "s"}` })), (k) => openDrawer({ kind: "domain", key: k }, state))),
      card(sectionTitle("users", "green", "Top clients", "Most active devices"),
        rankedList(dns.top_clients.map((c) => ({ key: c.client, label: c.name ? [c.name, h("span", { class: "mono" }, c.client)] : h("span", { class: "mono" }, c.client), title: c.client, value: c.count, sub: `${c.domains} domain${c.domains === 1 ? "" : "s"}` })), (k) => openDrawer({ kind: "client", key: k }, state))),
      card(sectionTitle("alert", "amber", "Problem domains", "Blocked, NXDOMAIN or failed"),
        rankedList(dns.problems.map((d) => ({ key: d.domain, label: d.domain, title: d.domain, value: d.problems, sub: `${d.clients} client${d.clients === 1 ? "" : "s"}` })), (k) => openDrawer({ kind: "domain", key: k }, state), "No problems in this range")));

    const recent = card(sectionTitle("list", "rose", "Recent router warnings", `${fmtInt(events.total)} in this range`),
      events.items.length
        ? h("ul", { class: "ranked" }, events.items.map((e) => h("li", null, h("div", { class: "row" },
          h("span", { class: "body" }, h("span", { class: "line" }, h("span", { class: "name" }, severityDot(e.severity), " ", h("span", { class: "mono muted" }, e.topics)), h("span", { class: "muted" }, fmtAgo(e.ts))), h("div", null, e.message))))))
        : h("p", { class: "empty" }, "No warnings or errors logged"),
      h("div", { style: { marginTop: "8px" } }, h("button", { class: "btn small", onclick: () => navigate("events", { severity: "problem" }) }, "All events")));

    const outcomesCard = card(sectionTitle("check", "green", "DNS outcomes", "As reported by the router"),
      segmentedBar(outcomes.map((o) => ({ key: o.filter, label: o.label, value: byOutcome[o.key], color: o.color, iconName: o.icon })), (k) => navigate("dns", { outcome: k })));

    return [issueList, tiles, chartRow, probeCard, lists, h("div", { class: "grid cols-2" }, outcomesCard, recent)];
  },
};

export function bucketText(secs) {
  return secs >= 3600 ? `${secs / 3600}h` : secs >= 60 ? `${secs / 60}min` : `${secs}s`;
}

export function throughputChart(net, state, height = 200) {
  const el = h("div");
  draw(() => {
    const series = [
      { label: "Download", color: "var(--viz-1)", points: bucketsToPoints(net.throughput.rx || []) },
      { label: "Upload", color: "var(--viz-2)", points: bucketsToPoints(net.throughput.tx || []) },
    ];
    lines(el, { series, bucketSecs: net.bucket_secs, height, yFormat: (v) => fmtBps(v), valueFormat: fmtBps });
    el.appendChild(legend(series, true));
  });
  return el;
}

export function probeChart(net, height = 160) {
  const el = h("div");
  draw(() => {
    const series = net.probes.map((p, i) => ({ label: `${p.target.kind} ${p.target.addr}`, color: seriesColors[i % seriesColors.length], points: bucketsToPoints(p.rtt) }));
    lines(el, { series, bucketSecs: net.bucket_secs, height, yFormat: (v) => fmtMs(v), valueFormat: fmtMs });
    el.appendChild(legend(series, true));
  });
  return el;
}
