export function url(path, params = {}) {
  const qs = new URLSearchParams();
  for (const [k, v] of Object.entries(params)) {
    if (v !== undefined && v !== null && v !== "") qs.set(k, String(v));
  }
  const s = qs.toString();
  return s ? `${path}?${s}` : path;
}

async function request(path, init) {
  const res = await fetch(path, init);
  if (!res.ok) {
    // A lost session sends the dashboard back to the sign-in page.
    if (res.status === 401 && !path.startsWith("/api/auth/") && location.pathname !== "/login") location.replace("/login");
    let msg = `${res.status} ${res.statusText}`;
    try {
      const body = await res.json();
      if (body && body.error) msg = body.error;
    } catch { /* keep status text */ }
    throw new Error(msg);
  }
  return res.json();
}

export function get(path, params, signal) {
  return request(url(path, params), { signal });
}

export function post(path, body) {
  return request(path, { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(body || {}) });
}

export const api = {
  dnsSummary: (range, limit, signal) => get("/api/dns/summary", { range, limit }, signal),
  dnsTimeline: (f, signal) => get("/api/dns/timeline", f, signal),
  dnsDomains: (f, limit, signal) => get("/api/dns/domains", { ...f, limit }, signal),
  dnsClients: (f, limit, signal) => get("/api/dns/clients", { ...f, limit }, signal),
  dnsQueries: (f, page, pageSize, signal) => get("/api/dns/queries", { ...f, page, page_size: pageSize }, signal),
  dnsResolve: (name, type, signal) => get("/api/dns/resolve", { name, type }, signal),
  dnsExportUrl: (f) => url("/api/dns/export.csv", f),
  metrics: (metric, key, range, signal) => get("/api/metrics", { metric, key, range }, signal),
  network: (range, iface, signal) => get("/api/network", { range, interface: iface }, signal),
  devices: (range, signal) => get("/api/devices", { range }, signal),
  events: (f, page, pageSize, signal) => get("/api/events", { ...f, page, page_size: pageSize }, signal),
  issues: (signal) => get("/api/issues", {}, signal),
  status: (signal) => get("/api/status", {}, signal),
  authStatus: (signal) => get("/api/auth/status", {}, signal),
  logout: () => post("/api/auth/logout"),
  changePassword: (current, next) => post("/api/auth/password", { current, new: next }),
};
