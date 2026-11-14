import { draw } from "../charts.js";
import { h, card, sectionTitle, kv } from "../dom.js";
import { api } from "../api.js";
import { fmtBps, fmtBytes, fmtMs, fmtInt, fmtPct, fmtAgo, fmtNum, fmtDuration } from "../format.js";
import { lines, legend, bucketsToPoints } from "../charts.js";
import { tile, table } from "../widgets.js";
import { throughputChart, probeChart, scaleToggle } from "./overview.js";
import { compareRange } from "../range.js";

function viewState(state) {
  state.views.network ||= { iface: "" };
  return state.views.network;
}

export const network = {
  async load(state, signal) {
    const v = viewState(state);
    const cmp = compareRange(state);
    const [net, prevNet] = await Promise.all([api.network(state.range, v.iface, signal), cmp ? api.network(cmp, v.iface, signal) : null]);
    return { net, prevNet };
  },

  render({ net, prevNet }, state, { openDrawer }) {
    const v = viewState(state);
    const sel = v.iface || net.wan;
    const last = (b) => { for (let i = (b || []).length - 1; i >= 0; i--) if (b[i].avg !== null) return b[i].avg; return null; };
    const peak = (b) => Math.max(0, ...(b || []).map((x) => x.max || 0));
    const rx = net.throughput.rx, tx = net.throughput.tx;
    const blocks = [];

    if (!net.router) {
      blocks.push(h("div", { class: "card" }, h("b", null, "Router polling is not configured."), " ",
        h("span", { class: "muted" }, "Set MIKROTIK_URL, MIKROTIK_USER and MIKROTIK_PASSWORD to collect interface throughput, system load, connections, device bandwidth and Wi-Fi signal.")));
    } else {
      blocks.push(h("div", { class: "tiles" },
        tile({ label: `Download on ${sel || "WAN"}`, value: fmtBps(last(rx)), detail: `Peak ${fmtBps(peak(rx))} in range`, iconName: "download", tone: "green" }),
        tile({ label: `Upload on ${sel || "WAN"}`, value: fmtBps(last(tx)), detail: `Peak ${fmtBps(peak(tx))} in range`, iconName: "upload", tone: "violet" }),
        tile({ label: "Connections", value: fmtNum(net.router.connections), detail: net.router.max_connections ? `of ${fmtNum(net.router.max_connections)} tracked` : "Tracked by the firewall", iconName: "link", tone: "blue" }),
        tile({ label: "CPU load", value: `${net.router.cpu_load}%`, detail: net.router.total_memory ? `${fmtPct(net.router.total_memory - net.router.free_memory, net.router.total_memory, 0)} memory used` : "", iconName: "cpu", tone: "sky" })));

      const ifaceSelect = h("select", { onchange: (e) => { v.iface = e.target.value; document.getElementById("refresh").click(); } },
        net.interfaces.filter((i) => i.running || i.rx_bytes > 0).map((i) => h("option", { value: i.name, selected: i.name === sel }, i.wan ? `${i.name} (WAN)` : i.name)));
      blocks.push(h("div", { class: "card" },
        h("div", { class: "card-head" }, sectionTitle("download", "green", "Throughput", "Average per bucket as the line, the bucket's peak as the thin line above it"), h("div", { class: "head-tools" }, scaleToggle(), ifaceSelect)),
        throughputChart(net, state, 240, prevNet)));

      blocks.push(card(sectionTitle("router", "violet", "Interfaces", `${net.interfaces.length} on ${net.router.identity}`),
        table([
          { label: "Interface", render: (i) => [h("button", { class: "link", onclick: () => openDrawer({ kind: "interface", key: i.name }, state) }, i.name), i.wan ? h("span", { class: "badge", style: { marginLeft: "6px" } }, "WAN") : null, i.comment ? h("span", { class: "muted" }, ` ${i.comment}`) : null] },
          { label: "Type", cls: "muted", render: (i) => [i.type, i.link_rate ? ` ${i.link_rate}` : ""] },
          { label: "State", render: (i) => h("span", { class: "status-pill" }, h("span", { class: "dot", style: { background: i.disabled ? "var(--neutral)" : i.running ? "var(--good)" : "var(--critical)" } }), i.disabled ? "disabled" : i.running ? "running" : "down") },
          { label: "Download", right: true, cls: "num", render: (i) => fmtBps(i.rx_bps) },
          { label: "Upload", right: true, cls: "num", render: (i) => fmtBps(i.tx_bps) },
          { label: "Received", right: true, cls: "num muted", render: (i) => fmtBytes(i.rx_bytes) },
          { label: "Sent", right: true, cls: "num muted", render: (i) => fmtBytes(i.tx_bytes) },
          { label: "Errors", right: true, cls: "num", render: (i) => (i.errors ? fmtInt(i.errors) : "-") },
          { label: "Drops", right: true, cls: "num muted", render: (i) => (i.drops ? fmtInt(i.drops) : "-") },
          { label: "Link downs", right: true, cls: "num", render: (i) => (i.link_downs ? fmtInt(i.link_downs) : "-") },
        ], net.interfaces, { onRow: (i) => openDrawer({ kind: "interface", key: i.name }, state) })));

      const sys = (key, label, fmt) => {
        const el = h("div");
        draw(() => lines(el, { series: [{ label, color: "var(--viz-1)", points: bucketsToPoints(net.system[key] || []) }], bucketSecs: net.bucket_secs, height: 150, yFormat: fmt, valueFormat: fmt, area: true }));
        return el;
      };
      blocks.push(h("div", { class: "grid cols-3" },
        card(sectionTitle("cpu", "sky", "CPU load", "Percent"), sys("cpu", "CPU", (x) => `${Math.round(x)}%`)),
        card(sectionTitle("server", "violet", "Memory used", "Percent of RAM"), sys("mem", "Memory", (x) => `${Math.round(x)}%`)),
        card(sectionTitle("link", "blue", "Connections", "Tracked by the firewall"), sys("connections", "Connections", fmtNum))));
    }

    const r = net.router;
    if (r && (r.wireguard || []).length) {
      blocks.push(card(sectionTitle("link", "violet", "WireGuard peers", `${r.wireguard.length} on ${new Set(r.wireguard.map((p) => p.interface)).size} interface${new Set(r.wireguard.map((p) => p.interface)).size === 1 ? "" : "s"}`),
        table([
          { label: "Peer", render: (p) => [h("b", null, p.name), h("span", { class: "muted" }, ` ${p.interface}`)] },
          { label: "Endpoint", cls: "mono muted", render: (p) => p.endpoint || "-" },
          { label: "Allowed", cls: "mono muted", render: (p) => p.allowed_address },
          { label: "Handshake", render: (p) => h("span", { class: "status-pill" }, h("span", { class: "dot", style: { background: p.disabled ? "var(--neutral)" : p.handshake_age_secs < 0 ? "var(--neutral)" : p.handshake_age_secs <= 180 ? "var(--good)" : p.handshake_age_secs <= 900 ? "var(--warning)" : "var(--critical)" } }), p.handshake_age_secs < 0 ? "never" : `${p.last_handshake} ago`) },
          { label: "Download", right: true, cls: "num", render: (p) => fmtBps(p.rx_bps) },
          { label: "Upload", right: true, cls: "num", render: (p) => fmtBps(p.tx_bps) },
          { label: "Received", right: true, cls: "num muted", render: (p) => fmtBytes(p.rx_bytes) },
          { label: "Sent", right: true, cls: "num muted", render: (p) => fmtBytes(p.tx_bytes) },
        ], r.wireguard)));
    }
    if (r && (r.netwatch || []).length) {
      blocks.push(card(sectionTitle("radio", "sky", "Netwatch", "Hosts watched by the router"),
        table([
          { label: "Host", render: (w) => [h("b", { class: "mono" }, w.host), w.comment ? h("span", { class: "muted" }, ` ${w.comment}`) : null] },
          { label: "Type", cls: "muted", render: (w) => w.type },
          { label: "Status", render: (w) => h("span", { class: "status-pill" }, h("span", { class: "dot", style: { background: w.status === "up" ? "var(--good)" : "var(--critical)" } }), w.status) },
          { label: "Since", cls: "mono muted", render: (w) => w.since },
        ], r.netwatch)));
    }
    if (net.probes.length) {
      blocks.push(card(sectionTitle("zap", "sky", "Reachability", "ICMP and DNS probes from the collector"),
        probeChart(net, 200),
        h("div", { class: "probe-grid", style: { marginTop: "14px" } }, net.probes.map((p) => {
          const lossEl = h("div");
          draw(() => lines(lossEl, { series: [{ label: "Loss", color: "var(--critical)", points: p.loss.map((b) => ({ ts: b.ts, v: b.avg === null ? null : b.avg * 100 })) }], bucketSecs: net.bucket_secs, height: 90, yFormat: (x) => `${Math.round(x)}%`, valueFormat: (x) => `${x.toFixed(1)}%`, area: true }));
          return h("div", { class: "probe" },
            h("div", { class: "head" }, h("b", { class: "mono" }, `${p.target.kind} ${p.target.addr}`),
              h("span", { class: "status-pill" }, h("span", { class: "dot", style: { background: p.up ? "var(--good)" : "var(--critical)" } }), p.up ? "up" : "down", h("span", { class: "muted" }, ` since ${fmtAgo(p.since)}`))),
            h("div", { class: "nums" }, h("span", null, "avg ", h("b", null, fmtMs(p.rtt_avg_ms))), h("span", null, "max ", h("b", null, fmtMs(p.rtt_max_ms))), h("span", null, "loss ", h("b", null, p.loss_pct === null ? "-" : `${p.loss_pct.toFixed(1)}%`)), h("span", null, "last ", h("b", null, fmtMs(p.last_rtt_ms)))),
            p.last_error ? h("div", { class: "muted mono", style: { marginBottom: "6px" } }, p.last_error) : null,
            lossEl);
        }))));
    }
    return blocks;
  },
};
