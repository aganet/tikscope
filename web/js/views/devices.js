import { h, icon, card, sectionTitle } from "../dom.js";
import { api } from "../api.js";
import { fmtBps, fmtInt, fmtAgo, fmtRate } from "../format.js";
import { table } from "../widgets.js";

function viewState(state) {
  state.views.devices ||= { search: "" };
  return state.views.devices;
}

export function wifiBars(dbm) {
  const level = dbm >= -55 ? 4 : dbm >= -65 ? 3 : dbm >= -75 ? 2 : dbm >= -85 ? 1 : 0;
  return h("span", { class: "wifi-bars", title: `${dbm} dBm` }, [4, 7, 10, 12].map((hgt, i) => h("i", { class: i < level ? "on" : "", style: { height: `${hgt}px` } })));
}

export const devices = {
  async load(state, signal) {
    return api.devices(state.range, signal);
  },

  render(list, state, { openDrawer }) {
    const v = viewState(state);
    const q = v.search.trim().toLowerCase();
    const rows = q ? list.filter((d) => [...(d.ips || [d.ip]), d.name, d.mac].some((x) => (x || "").toLowerCase().includes(q))) : list;
    const hasBandwidth = list.some((d) => d.rx_avg_bps !== null);
    const hasWifi = list.some((d) => d.wifi);
    let debounce;
    const columns = [
      { label: "Device", render: (d) => [h("b", null, d.name || h("span", { class: "muted" }, "unnamed")), d.wifi ? h("span", { class: "muted", title: `Wi-Fi ${d.wifi.interface}` }, " ", icon("wifi", "inline-icon")) : null] },
      { label: "Address", cls: "mono nowrap", render: (d) => [d.ip, d.ips && d.ips.length > 1 ? h("span", { class: "muted", title: d.ips.join("\n") }, ` +${d.ips.length - 1}`) : null] },
      { label: "MAC", cls: "mono muted nowrap", render: (d) => d.mac || "-" },
      hasWifi && { label: "Wi-Fi", cls: "nowrap", render: (d) => (d.wifi ? [wifiBars(d.wifi.signal_dbm), h("span", { class: "muted mono" }, `${d.wifi.signal_dbm} dBm`), d.wifi.tx_rate ? h("span", { class: "muted" }, ` ${fmtRate(d.wifi.tx_rate)}`) : null] : h("span", { class: "muted" }, "-")) },
      { label: "DNS queries", right: true, cls: "num", render: (d) => fmtInt(d.queries) },
      { label: "Problems", right: true, cls: "num", render: (d) => (d.problems ? fmtInt(d.problems) : "-") },
      hasBandwidth && { label: "Download", right: true, cls: "num", render: (d) => fmtBps(d.rx_bps) },
      hasBandwidth && { label: "Upload", right: true, cls: "num", render: (d) => fmtBps(d.tx_bps) },
      hasBandwidth && { label: "Avg download", right: true, cls: "num muted", render: (d) => fmtBps(d.rx_avg_bps) },
      { label: "Last DNS", right: true, cls: "muted nowrap", render: (d) => (d.last_seen ? fmtAgo(d.last_seen) : "-") },
      { label: "Source", cls: "muted", render: (d) => d.source || "dns" },
    ].filter(Boolean);
    return card(h("div", { class: "section-title", style: { flex: 1 } }, sectionTitle("users", "green", "Devices", `${fmtInt(list.length)} known, names from DHCP${hasBandwidth ? ", bandwidth from Kid Control" : ""}`)),
      h("div", { class: "filters" },
        h("input", { type: "search", placeholder: "Filter by name, address or MAC", value: v.search, oninput: (e) => { v.search = e.target.value; clearTimeout(debounce); debounce = setTimeout(() => document.getElementById("refresh").click(), 250); } }),
        !hasBandwidth ? h("span", { class: "muted", style: { fontSize: "12px" } }, "Per-device bandwidth needs Kid Control on the router: /ip kid-control device add name=all mac-address=00:00:00:00:00:00 dynamic=yes") : null),
      table(columns, rows, { onRow: (d) => openDrawer({ kind: "client", key: d.ip }, state), empty: "No devices match" }));
  },
};
