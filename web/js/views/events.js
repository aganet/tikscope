import { h, icon, card, sectionTitle } from "../dom.js";
import { api } from "../api.js";
import { fmtInt, fmtDateTime } from "../format.js";
import { table, pager, severityDot } from "../widgets.js";

const severityOptions = [["", "Any severity"], ["problem", "Warning and above"], ["critical", "Critical"], ["error", "Error"], ["warning", "Warning"], ["info", "Info"]];

function viewState(state) {
  state.views.events ||= { severity: "", search: "", page: 1, pageSize: 50 };
  const params = new URLSearchParams((location.hash.split("?")[1]) || "");
  if (params.has("severity")) { state.views.events.severity = params.get("severity"); state.views.events.page = 1; history.replaceState(null, "", "#events"); }
  return state.views.events;
}

export const events = {
  async load(state, signal) {
    const v = viewState(state);
    return api.events({ range: state.range, severity: v.severity, search: v.search }, v.page, v.pageSize, signal);
  },

  render(data, state) {
    const v = viewState(state);
    const refresh = () => document.getElementById("refresh").click();
    let debounce;
    const counts = data.counts || {};
    const summary = ["critical", "error", "warning", "info"].map((s) => h("button", { class: "badge", style: { cursor: "pointer" }, onclick: () => { v.severity = s; v.page = 1; refresh(); } }, severityDot(s), h("b", { class: "num" }, fmtInt(counts[s] || 0))));
    return card(sectionTitle("list", "rose", "Router log", `${fmtInt(data.total)} events match`),
      h("div", { class: "filters" },
        h("input", { type: "search", placeholder: "Message or topic contains", value: v.search, oninput: (e) => { v.search = e.target.value; v.page = 1; clearTimeout(debounce); debounce = setTimeout(refresh, 350); } }),
        h("select", { onchange: (e) => { v.severity = e.target.value; v.page = 1; refresh(); } }, severityOptions.map(([val, l]) => h("option", { value: val, selected: val === v.severity }, l))),
        (v.search || v.severity) ? h("button", { class: "btn", onclick: () => { v.search = ""; v.severity = ""; v.page = 1; refresh(); } }, icon("x"), "Clear") : null,
        h("span", { class: "spacer" }),
        h("div", { style: { display: "flex", gap: "6px", flexWrap: "wrap" } }, summary)),
      table([
        { label: "Time", cls: "mono nowrap", render: (e) => fmtDateTime(e.ts) },
        { label: "Severity", render: (e) => severityDot(e.severity) },
        { label: "Topics", cls: "mono muted nowrap", render: (e) => e.topics || "-" },
        { label: "Message", render: (e) => e.message },
        { label: "Source", cls: "mono muted nowrap", render: (e) => e.router },
      ], data.items, { empty: "No events in this range. Send more topics to the collector: /system logging add topics=info,error,warning,critical action=tikscope" }),
      pager({ page: data.page, total: data.total, pageSize: data.page_size, onPage: (p) => { v.page = p; refresh(); }, label: "events" }));
  },
};
