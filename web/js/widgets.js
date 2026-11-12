// Reusable view pieces built on dom.js: tiles, ranked lists, segmented bars,
// badges, tables and pagers.
import { h, icon, card } from "./dom.js";
import { fmtInt, fmtPct, fmtDateTime, fmtMs, outcomeLabel, outcomeOf, outcomes, severityColor } from "./format.js";

export function tile({ label, value, detail, iconName, tone, spark, onClick }) {
  const el = h("div", { class: `card tile${onClick ? " clickable" : ""}`, role: onClick ? "button" : null, tabindex: onClick ? 0 : null },
    h("span", { class: `chip ${tone}` }, icon(iconName)),
    h("div", { class: "body" },
      h("div", { class: "label" }, label),
      h("div", { class: "value" }, value),
      detail ? h("div", { class: "detail" }, detail) : null,
      spark ? h("div", { class: "spark" }, spark) : null));
  if (onClick) {
    el.addEventListener("click", onClick);
    el.addEventListener("keydown", (e) => { if (e.key === "Enter" || e.key === " ") { e.preventDefault(); onClick(); } });
  }
  return el;
}

// items: [{key, label (node or string), value, sub, title}]
export function rankedList(items, onSelect, empty = "No data in this range") {
  if (!items.length) return h("p", { class: "empty" }, empty);
  const max = Math.max(...items.map((i) => i.value), 1);
  return h("ol", { class: "ranked" }, items.map((item, i) => {
    const body = [
      h("span", { class: "rank num" }, i + 1),
      h("span", { class: "body" },
        h("span", { class: "line" }, h("span", { class: "name", title: item.title || "" }, item.label), h("span", { class: "num" }, fmtInt(item.value))),
        h("span", { class: "bar" }, h("i", { style: { width: `${(item.value / max) * 100}%` } })),
        item.sub ? h("span", { class: "meta" }, item.sub) : null),
    ];
    return h("li", null, onSelect ? h("button", { type: "button", onclick: () => onSelect(item.key) }, body) : h("div", { class: "row" }, body));
  }));
}

// segments: [{key, label, value, color, iconName}]
export function segmentedBar(segments, onSelect) {
  const total = segments.reduce((s, p) => s + p.value, 0);
  const visible = segments.filter((s) => s.value > 0);
  const bar = h("div", { class: "segbar", role: "img", "aria-label": visible.map((s) => `${s.label} ${fmtPct(s.value, total)}`).join(", ") },
    visible.map((s) => h("i", { style: { width: `${(s.value / total) * 100}%`, background: s.color } })),
    total === 0 ? h("i", { style: { width: "100%", background: "var(--surface-2)" } }) : null);
  const list = h("ul", { class: "seglist" }, segments.map((s) => {
    const inner = [h("span", { class: "sw", style: { background: s.color } }), s.iconName ? icon(s.iconName) : null,
      h("span", { class: "lbl" }, s.label), h("span", { class: "num" }, fmtInt(s.value)), h("span", { class: "pct num" }, fmtPct(s.value, total))];
    return h("li", null, onSelect ? h("button", { type: "button", onclick: () => onSelect(s.key) }, inner) : inner);
  }));
  return h("div", null, bar, list);
}

export function outcomeBadge(outcome) {
  const o = outcomes.find((s) => s.key === outcomeOf(outcome));
  return h("span", { class: "badge", title: outcomeLabel[outcome] }, h("span", { style: { color: o.color, display: "inline-flex" } }, icon(o.icon)), outcomeLabel[outcome]);
}

export function severityDot(sev) {
  return h("span", { class: "status-pill" }, h("span", { class: "dot", style: { background: severityColor[sev] || "var(--neutral)" } }), sev);
}

export function table(columns, rows, { onRow, empty = "Nothing to show", cls } = {}) {
  return h("div", { class: "table-wrap" }, h("table", { class: cls },
    h("thead", null, h("tr", null, columns.map((c) => h("th", { class: c.right ? "r" : null, style: c.width ? { width: c.width } : null }, c.label)))),
    h("tbody", null, rows.length ? rows.map((r) => {
      const tr = h("tr", { class: onRow ? "clickable" : null }, columns.map((c) => h("td", { class: [c.right ? "r" : "", c.cls || ""].join(" ") }, c.render(r))));
      if (onRow) tr.addEventListener("click", (e) => { if (!e.target.closest("button, a")) onRow(r); });
      return tr;
    }) : h("tr", null, h("td", { colspan: columns.length, class: "empty" }, empty)))));
}

export function pager({ page, total, pageSize, onPage, label = "rows" }) {
  const pages = Math.max(1, Math.ceil(total / pageSize));
  return h("div", { class: "pager" },
    h("span", { class: "num" }, `${fmtInt(total)} ${label}`),
    h("div", { class: "buttons" },
      h("button", { class: "btn small", disabled: page <= 1, onclick: () => onPage(page - 1) }, "Previous"),
      h("span", { class: "num" }, `Page ${page} of ${fmtInt(pages)}`),
      h("button", { class: "btn small", disabled: page >= pages, onclick: () => onPage(page + 1) }, "Next")));
}

// Query log rows shared by the DNS view and the drawers.
export function queriesTable(rows, { hideClient, hideDomain, onClient, onDomain, loading }) {
  const columns = [
    { label: "Time", cls: "mono nowrap", render: (q) => fmtDateTime(q.ts) },
    !hideClient && { label: "Client", cls: "nowrap", render: (q) => [h("button", { class: "link mono", onclick: () => onClient(q.client), title: "Show this client" }, q.client), q.name ? h("span", { class: "muted" }, ` ${q.name}`) : null] },
    !hideDomain && { label: "Domain", cls: "trunc", render: (q) => h("button", { class: "link", onclick: () => onDomain(q.domain), title: q.domain }, q.domain) },
    { label: "Type", render: (q) => h("span", { class: "badge mono" }, q.type) },
    { label: "Outcome", render: (q) => outcomeBadge(q.outcome) },
    { label: "Answer", cls: "mono trunc muted", render: (q) => h("span", { title: q.answer }, q.answer) },
    { label: "Latency", right: true, cls: "mono muted", render: (q) => fmtMs(q.latency_ms) },
  ].filter(Boolean);
  return table(columns, rows, { empty: loading ? "Loading" : "No queries match" });
}

export { card };
