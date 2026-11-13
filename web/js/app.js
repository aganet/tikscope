// Entry point: global controls, hash routing, polling and the drawer.
import { h, icon, replace, clear } from "./dom.js";
import { hideTooltip } from "./charts.js";
import { overview } from "./views/overview.js";
import { dns } from "./views/dns.js";
import { network } from "./views/network.js";
import { devices } from "./views/devices.js";
import { events } from "./views/events.js";
import { system } from "./views/system.js";
import { wall } from "./views/wall.js";
import { openDrawer, closeDrawer, drawerOpen } from "./drawer.js";
import { api } from "./api.js";

const views = { overview, dns, network, devices, events, system, wall };

export const state = {
  range: "24h",
  autoRefresh: true,
  interval: 10,
  tab: "overview",
  // Per-view state (filters, pages) survives refreshes and tab switches.
  views: {},
};

const main = document.getElementById("main");
let current = null; // {name, ctrl}
let timer = null;

function readLocation() {
  const hash = location.hash.slice(1);
  const [tab, qs] = hash.split("?");
  const params = new URLSearchParams(qs || "");
  return { tab: views[tab] ? tab : "overview", params };
}

export function navigate(tab, params) {
  const qs = params ? new URLSearchParams(params).toString() : "";
  location.hash = tab + (qs ? `?${qs}` : "");
}

async function render(reason = "refresh") {
  const { tab } = readLocation();
  if (current && current.name !== tab && current.ctrl) current.ctrl.abort();
  const ctrl = new AbortController();
  current = { name: tab, ctrl };
  state.tab = tab;
  for (const a of document.querySelectorAll("#tabs a")) a.classList.toggle("active", a.dataset.tab === tab);
  document.body.classList.toggle("wall", tab === "wall");
  try {
    const view = views[tab];
    const data = await view.load(state, ctrl.signal);
    if (ctrl.signal.aborted) return;
    hideTooltip();
    await swapView(view.render(data, state, { navigate, openDrawer }));
    document.getElementById("updated").textContent = `Updated ${new Date().toLocaleTimeString()}`;
    main.dataset.lastData = "1";
    if (drawerOpen()) openDrawer(null, state, { refresh: true });
  } catch (err) {
    if (ctrl.signal.aborted) return;
    if (reason === "initial" || !main.dataset.lastData) replace(main, h("div", { class: "error-banner" }, `Could not load data: ${err.message}`));
    document.getElementById("updated").textContent = `Error: ${err.message}`;
  }
}

// The new view is laid out hidden next to the old one so its charts can
// measure themselves and draw, then the two are swapped before the next
// paint. A refresh never shows an empty or half-drawn page.
async function swapView(content) {
  const next = h("div", { class: "view pending" }, content);
  main.appendChild(next);
  await new Promise(requestAnimationFrame);
  for (const old of [...main.children]) if (old !== next) old.remove();
  next.classList.remove("pending");
}

function schedule() {
  clearInterval(timer);
  timer = null;
  if (!state.autoRefresh) return;
  timer = setInterval(() => { if (document.visibilityState === "visible") render(); }, state.interval * 1000);
}

function applyTheme(theme) {
  if (theme) document.documentElement.dataset.theme = theme;
  else delete document.documentElement.dataset.theme;
  const dark = theme === "dark" || (!theme && matchMedia("(prefers-color-scheme: dark)").matches);
  replace(document.getElementById("theme"), icon(dark ? "sun" : "moon"));
}

function setupControls() {
  const range = document.getElementById("range");
  const interval = document.getElementById("interval");
  const pause = document.getElementById("pause");
  const box = document.getElementById("refresh-box");
  try {
    state.range = localStorage.getItem("tikscope.range") || state.range;
    state.interval = Number(localStorage.getItem("tikscope.interval")) || state.interval;
    applyTheme(localStorage.getItem("tikscope.theme") || "");
  } catch { applyTheme(""); }
  range.value = state.range;
  interval.value = String(state.interval);
  const paintPause = () => {
    replace(pause, icon(state.autoRefresh ? "pause" : "play"));
    pause.title = state.autoRefresh ? "Pause auto refresh" : "Resume auto refresh";
    box.classList.toggle("paused", !state.autoRefresh);
  };
  paintPause();
  range.addEventListener("change", () => { state.range = range.value; try { localStorage.setItem("tikscope.range", state.range); } catch {} render(); });
  interval.addEventListener("change", () => { state.interval = Number(interval.value); try { localStorage.setItem("tikscope.interval", state.interval); } catch {} schedule(); });
  pause.addEventListener("click", () => { state.autoRefresh = !state.autoRefresh; paintPause(); schedule(); });
  const refresh = document.getElementById("refresh");
  replace(refresh, icon("refresh"));
  refresh.addEventListener("click", () => render());
  document.getElementById("theme").addEventListener("click", () => {
    const cur = document.documentElement.dataset.theme;
    const dark = cur === "dark" || (!cur && matchMedia("(prefers-color-scheme: dark)").matches);
    const next = dark ? "light" : "dark";
    try { localStorage.setItem("tikscope.theme", next); } catch {}
    applyTheme(next);
  });
  const logout = document.getElementById("logout");
  replace(logout, icon("logout"));
  logout.addEventListener("click", async () => {
    try { await api.logout(); } catch { /* the cookie is gone either way */ }
    location.replace("/login");
  });
  replace(document.getElementById("drawer-close"), icon("x"));
  document.getElementById("drawer-close").addEventListener("click", closeDrawer);
  document.getElementById("drawer-backdrop").addEventListener("click", closeDrawer);
  document.addEventListener("keydown", (e) => { if (e.key === "Escape") closeDrawer(); });
}

// Re-render is cheap and charts size themselves from their container, so a
// resize just draws the current view again.
let resizeTimer;
window.addEventListener("resize", () => { clearTimeout(resizeTimer); resizeTimer = setTimeout(() => render(), 200); });
window.addEventListener("hashchange", () => {
  const { tab, params } = readLocation();
  if (params.get("client")) openDrawer({ kind: "client", key: params.get("client") }, state);
  else if (params.get("domain")) openDrawer({ kind: "domain", key: params.get("domain") }, state);
  else if (params.get("interface")) openDrawer({ kind: "interface", key: params.get("interface") }, state);
  if (!current || current.name !== tab || !params.toString()) render();
});

// The header names the router the page is showing, so a demo instance and a
// live one are never confused.
async function showRouter() {
  try {
    const [s, who] = await Promise.all([api.status(), api.authStatus()]);
    const r = s.router;
    const el = document.getElementById("router-name");
    el.textContent = r && r.identity ? `${r.identity} (${r.board}, RouterOS ${r.version}), ` : "no router configured, ";
    document.title = r && r.identity ? `tikscope: ${r.identity}` : "tikscope";
    document.getElementById("logout").title = who.username ? `Sign out ${who.username}` : "Sign out";
  } catch { /* status is retried on the next refresh */ }
}

setupControls();
showRouter();
setInterval(showRouter, 60000);
render("initial");
schedule();
const { params } = readLocation();
if (params.get("client")) openDrawer({ kind: "client", key: params.get("client") }, state);
else if (params.get("domain")) openDrawer({ kind: "domain", key: params.get("domain") }, state);
else if (params.get("interface")) openDrawer({ kind: "interface", key: params.get("interface") }, state);
clear(document.getElementById("toast"));
