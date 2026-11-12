// Wall mode: a dense, full-width layout for a TV or a spare monitor.
import { draw, stackedBars, lines, legend, bucketsToPoints } from "../charts.js";
import { h, icon, card, sectionTitle } from "../dom.js";
import { api } from "../api.js";
import { fmtNum, fmtPct, fmtMs, fmtBps, fmtAgo, outcomes, outcomeTotals } from "../format.js";
import { rankedList, segmentedBar, severityDot } from "../widgets.js";
import { networkNow, interfaceCards, probeChart } from "./overview.js";

export const wall = {
  async load(state, signal) {
    const [dns, net, issues, devices, events] = await Promise.all([
      api.dnsSummary(state.range, 6, signal),
      api.network(state.range, "", signal),
      api.issues(signal),
      api.devices(state.range, signal),
      api.events({ range: state.range, severity: "problem" }, 1, 6, signal),
    ]);
    return { dns, net, issues, devices, events };
  },

  render({ dns, net, issues, devices, events }, state, { navigate, openDrawer }) {
    const t = dns.totals;
    const byOutcome = outcomeTotals(t.outcomes);
    const problems = byOutcome.nxdomain + byOutcome.blocked + byOutcome.failed;
    const r = net.router;
    const strip = h("div", { class: "wall-top" },
      h("div", { class: "wall-title" }, h("span", { class: "brand-mark small", "aria-hidden": "true" }),
        h("b", null, r && r.identity ? r.identity : "tikscope"), r && r.board ? h("span", { class: "muted" }, ` ${r.board}, RouterOS ${r.version}, up ${r.uptime}`) : null),
      h("div", { class: "wall-issues" }, issues.length
        ? issues.slice(0, 3).map((i) => h("span", { class: `wall-issue ${i.severity}`, title: i.detail }, h("i", { class: `sev sev-${i.severity}` }), i.title))
        : h("span", { class: "wall-issue ok" }, icon("check"), "No issues")),
      h("div", { class: "wall-actions" },
        h("span", { class: "wall-clock", id: "wall-clock" }, new Date().toLocaleTimeString()),
        h("button", { class: "icon-btn", title: "Fullscreen", onclick: () => (document.fullscreenElement ? document.exitFullscreen() : document.documentElement.requestFullscreen()) }, icon("external")),
        h("button", { class: "btn small", onclick: () => navigate("overview") }, "Exit")));

    const dnsChart = h("div");
    draw(() => {
      const present = outcomes.filter((o) => dns.timeline.some((b) => b[o.key] > 0));
      const series = present.length ? present : [outcomes[0]];
      stackedBars(dnsChart, { buckets: dns.timeline, bucketSecs: dns.bucket_secs, series, height: 150 });
      if (series.length > 1) dnsChart.appendChild(legend(series));
    });
    const dnsCard = card(sectionTitle("activity", "blue", "DNS", `${fmtNum(t.queries)} queries, ${fmtPct(problems, t.queries)} problems, ${fmtMs(t.latency_ms)} response`),
      dnsChart,
      h("div", { style: { marginTop: "8px" } }, segmentedBar(outcomes.map((o) => ({ key: o.filter, label: o.label, value: byOutcome[o.key], color: o.color, iconName: o.icon })))));

    const talkers = devices.filter((d) => d.rx_avg_bps !== null).sort((a, b) => (b.rx_avg_bps || 0) - (a.rx_avg_bps || 0)).slice(0, 6);
    const talkerCard = card(sectionTitle("users", "green", talkers.length ? "Top devices by bandwidth" : "Top clients", talkers.length ? "Average download in range" : "DNS queries"),
      talkers.length
        ? rankedList(talkers.map((d) => ({ key: d.ip, label: d.name ? [d.name, h("span", { class: "mono" }, d.ip)] : h("span", { class: "mono" }, d.ip), title: d.ip, value: Math.round((d.rx_avg_bps || 0) / 1000), sub: `${fmtBps(d.rx_avg_bps)} down, ${fmtBps(d.tx_avg_bps)} up` })), (k) => openDrawer({ kind: "client", key: k }, state))
        : rankedList(dns.top_clients.map((c) => ({ key: c.client, label: c.name ? [c.name, h("span", { class: "mono" }, c.client)] : h("span", { class: "mono" }, c.client), title: c.client, value: c.count })), (k) => openDrawer({ kind: "client", key: k }, state)));
    const domainCard = card(sectionTitle("globe", "blue", "Top domains", "Most requested names"),
      rankedList(dns.top_domains.map((d) => ({ key: d.domain, label: d.domain, title: d.domain, value: d.count })), (k) => openDrawer({ kind: "domain", key: k }, state)));
    const eventCard = card(sectionTitle("list", "rose", "Router warnings", `${fmtNum(events.total)} in range`),
      events.items.length
        ? h("ul", { class: "ranked" }, events.items.map((e) => h("li", null, h("div", { class: "row" }, h("span", { class: "body" },
          h("span", { class: "line" }, h("span", { class: "name" }, severityDot(e.severity), " ", h("span", { class: "mono muted" }, e.topics)), h("span", { class: "muted" }, fmtAgo(e.ts))),
          h("div", { class: "wall-event" }, e.message))))))
        : h("p", { class: "empty" }, "No warnings or errors logged"));

    const probeStats = h("div", { class: "wall-probes" }, net.probes.map((p) => h("div", { class: "probe" },
      h("div", { class: "head" }, h("b", { class: "mono" }, `${p.target.kind} ${p.target.addr}`),
        h("span", { class: "status-pill" }, h("span", { class: "dot", style: { background: p.up ? "var(--good)" : "var(--critical)" } }), p.up ? "up" : "down")),
      h("div", { class: "nums" }, h("span", null, "avg ", h("b", null, fmtMs(p.rtt_avg_ms))), h("span", null, "max ", h("b", null, fmtMs(p.rtt_max_ms))), h("span", null, "loss ", h("b", null, p.loss_pct === null ? "-" : `${p.loss_pct.toFixed(1)}%`))))));
    const sys = (key, label, fmt, max) => {
      const el = h("div");
      draw(() => lines(el, { series: [{ label, color: "var(--viz-1)", points: bucketsToPoints(net.system[key] || []) }], bucketSecs: net.bucket_secs, height: 100, yFormat: fmt, valueFormat: fmt, area: true, maxY: max }));
      return h("div", { class: "wall-sys" }, h("h3", null, label), el);
    };
    const systemRow = r && r.identity ? h("div", { class: "wall-sysrow" },
      sys("cpu", "CPU load", (x) => `${Math.round(x)}%`, 100), sys("mem", "Memory used", (x) => `${Math.round(x)}%`, 100), sys("connections", "Connections", fmtNum)) : null;
    const top = h("div", { class: "wall-row top" },
      r && r.identity ? networkNow(net, state, { navigate, openDrawer }, 230) : card(null, h("p", { class: "empty" }, "Router polling is not configured")),
      card(sectionTitle("zap", "sky", "Reachability and health", net.probes.length ? "Round trip time from the collector, router load" : "No probes configured"),
        net.probes.length ? probeChart(net, 200) : h("p", { class: "empty" }, "Set PROBE_ICMP and PROBE_DNS"),
        net.probes.length ? probeStats : null,
        systemRow));
    const bottom = h("div", { class: "wall-row bottom" },
      r && r.identity ? interfaceCards(net, state, openDrawer, 6, 90) : null,
      dnsCard, talkerCard, domainCard, eventCard);
    return [strip, top, bottom];
  },
};

setInterval(() => {
  const el = document.getElementById("wall-clock");
  if (el) el.textContent = new Date().toLocaleTimeString();
}, 1000);
