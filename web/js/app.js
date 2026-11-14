// Entry point: global controls, hash routing, polling and the drawer.
import { h, icon, replace, clear } from "./dom.js";
import { hideTooltip, onZoom, interacting } from "./charts.js";
import { fmtDateTime } from "./format.js";
import { PRESET_SECS, rangeWindow } from "./range.js";
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

// range is what the API gets: a preset such as "24h", or "from..to" in unix
// seconds while zoomed into a chart. Only the preset is remembered; a zoom
// travels in the URL. compare is "" or a key of COMPARE in range.js.
export const state = {
  range: "24h",
  preset: "24h",
  compare: "",
  autoRefresh: true,
  interval: 10,
  tab: "overview",
  // Per-view state (filters, pages) survives refreshes and tab switches.
  views: {},
};

const main = document.getElementById("main");
let current = null; // {name, ctrl}
let timer = null;
const inflight = new Set(); // controllers of loads still running
let seq = 0, painted = 0; // render sequence numbers: started, last painted

function readLocation() {
  const hash = location.hash.slice(1);
  const [tab, qs] = hash.split("?");
  const params = new URLSearchParams(qs || "");
  return { tab: views[tab] ? tab : "overview", params };
}

function zoomed() {
  return state.range !== state.preset;
}

// Every hash carries the zoom window so a link or a tab switch keeps it.
function hashFor(tab, params) {
  const p = new URLSearchParams(params || "");
  if (zoomed()) p.set("range", state.range);
  else p.delete("range");
  const qs = p.toString();
  return `#${tab}${qs ? `?${qs}` : ""}`;
}

export function navigate(tab, params) {
  location.hash = hashFor(tab, params).slice(1);
}

export function replaceLocation(tab, params) {
  history.replaceState(null, "", hashFor(tab, params));
}

export function pushLocation(tab, params) {
  history.pushState(null, "", hashFor(tab, params));
}

function selectionFrom(params) {
  for (const kind of ["client", "domain", "interface"]) if (params.get(kind)) return { kind, key: params.get(kind) };
  return null;
}

function paintRange() {
  const range = document.getElementById("range");
  let opt = range.querySelector("option.zoom");
  if (zoomed()) {
    const { from, to } = rangeWindow(state.range);
    if (!opt) opt = range.appendChild(h("option", { class: "zoom" }));
    opt.value = state.range;
    opt.textContent = `${fmtDateTime(from, false)} to ${fmtDateTime(to, false)}`;
  } else if (opt) opt.remove();
  range.value = state.range;
  for (const id of ["zoom-out", "pan-left", "pan-right"]) document.getElementById(id).hidden = !zoomed();
  for (const a of document.querySelectorAll("#tabs a")) a.href = hashFor(a.dataset.tab);
}

// A range from the URL: a zoom window or a preset. Returns whether it changed.
function applyRange(r) {
  if (!r || r === state.range) return false;
  const [from, to] = r.split("..").map(Number);
  if (Number.isFinite(from) && Number.isFinite(to) && to > from) state.range = r;
  else if (PRESET_SECS[r]) state.preset = state.range = r;
  else return false;
  paintRange();
  return true;
}

function setRange(range) {
  state.range = range;
  paintRange();
  const { tab, params } = readLocation();
  replaceLocation(tab, params);
  render();
}

function zoomTo(from, to) {
  setRange(`${from}..${Math.max(to, from + 60)}`);
}

function zoomOut() {
  setRange(state.preset);
}

// Shift the zoom window by half its width, never past now.
function pan(dir) {
  const { from, to } = rangeWindow(state.range);
  const width = to - from, now = Math.floor(Date.now() / 1000);
  const a = Math.min(from + dir * Math.round(width / 2), now - width);
  if (a !== from) zoomTo(a, a + width);
}

// A refresh swaps the page in place once the data is there. A tab change
// clears the old page at once, or on a slow link it looks like the click
// did not take; the progress bar runs while anything loads.
async function render(reason = "refresh") {
  const { tab } = readLocation();
  const switching = current && current.name !== tab;
  // Every load of the old page is cancelled, not just the newest: a slow
  // one finishing later must never paint over the page the user chose.
  if (switching) for (const c of inflight) c.abort();
  const ctrl = new AbortController();
  inflight.add(ctrl);
  const mine = ++seq;
  current = { name: tab, ctrl };
  state.tab = tab;
  for (const a of document.querySelectorAll("#tabs a")) a.classList.toggle("active", a.dataset.tab === tab);
  document.body.classList.toggle("wall", tab === "wall");
  if (switching) {
    hideTooltip();
    replace(main, h("div", { class: "view" }, h("p", { class: "loading" }, `Loading ${document.querySelector(`#tabs a[data-tab="${tab}"]`).textContent}`)));
  }
  document.body.classList.add("loading");
  try {
    const view = views[tab];
    const data = await view.load(state, ctrl.signal);
    // Stale: cancelled, the page changed meanwhile, or a newer load of this
    // page has already been painted.
    if (ctrl.signal.aborted || readLocation().tab !== tab || mine < painted) return;
    hideTooltip();
    painted = mine;
    await swapView(view.render(data, state, { navigate, openDrawer }));
    document.getElementById("updated").textContent = `Updated ${new Date().toLocaleTimeString()}`;
    main.dataset.lastData = "1";
    if (drawerOpen()) openDrawer(null, state, { refresh: true });
  } catch (err) {
    if (ctrl.signal.aborted) return;
    if (reason === "initial" || switching || !main.dataset.lastData) replace(main, h("div", { class: "error-banner" }, `Could not load data: ${err.message}`));
    document.getElementById("updated").textContent = `Error: ${err.message}`;
  } finally {
    inflight.delete(ctrl);
    if (inflight.size === 0) document.body.classList.remove("loading");
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
  // A tick is skipped while the last load is still running, so a slow link
  // never stacks up requests.
  timer = setInterval(() => { if (document.visibilityState === "visible" && !interacting() && inflight.size === 0) render(); }, state.interval * 1000);
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
  const compare = document.getElementById("compare");
  try {
    state.preset = state.range = localStorage.getItem("tikscope.range") || state.range;
    state.interval = Number(localStorage.getItem("tikscope.interval")) || state.interval;
    state.compare = localStorage.getItem("tikscope.compare") || "";
    applyTheme(localStorage.getItem("tikscope.theme") || "");
  } catch { applyTheme(""); }
  interval.value = String(state.interval);
  compare.value = state.compare;
  compare.addEventListener("change", () => { state.compare = compare.value; try { localStorage.setItem("tikscope.compare", state.compare); } catch {} render(); });
  applyRange(readLocation().params.get("range"));
  paintRange();
  const paintPause = () => {
    replace(pause, icon(state.autoRefresh ? "pause" : "play"));
    pause.title = state.autoRefresh ? "Pause auto refresh" : "Resume auto refresh";
    box.classList.toggle("paused", !state.autoRefresh);
  };
  paintPause();
  range.addEventListener("change", () => {
    if (range.value === state.range) return;
    state.preset = range.value;
    try { localStorage.setItem("tikscope.range", state.preset); } catch {}
    zoomOut();
  });
  const zoomOutBtn = document.getElementById("zoom-out"), panLeft = document.getElementById("pan-left"), panRight = document.getElementById("pan-right");
  replace(zoomOutBtn, icon("x"));
  replace(panLeft, icon("left"));
  replace(panRight, icon("right"));
  onZoom({ into: zoomTo, out: zoomOut });
  zoomOutBtn.addEventListener("click", zoomOut);
  panLeft.addEventListener("click", () => pan(-1));
  panRight.addEventListener("click", () => pan(1));
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
  document.addEventListener("keydown", (e) => {
    if (e.key === "Escape") closeDrawer();
    if (!zoomed() || /^(INPUT|SELECT|TEXTAREA)$/.test(e.target.tagName)) return;
    if (e.key === "ArrowLeft") pan(-1);
    if (e.key === "ArrowRight") pan(1);
  });
}

// Re-render is cheap and charts size themselves from their container, so a
// resize just draws the current view again.
let resizeTimer;
window.addEventListener("resize", () => { clearTimeout(resizeTimer); resizeTimer = setTimeout(() => render(), 200); });
window.addEventListener("hashchange", () => {
  const { tab, params } = readLocation();
  let changed = applyRange(params.get("range"));
  if (!params.get("range") && zoomed()) { state.range = state.preset; paintRange(); changed = true; }
  const sel = selectionFrom(params);
  const wasOpen = drawerOpen();
  if (sel) openDrawer(sel, state, { fromHistory: true });
  else if (wasOpen) closeDrawer({ fromHistory: true });
  if (!current || current.name !== tab || changed || (!sel && !wasOpen)) render();
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
const initial = selectionFrom(readLocation().params);
if (initial) openDrawer(initial, state, { fromHistory: true });
clear(document.getElementById("toast"));
