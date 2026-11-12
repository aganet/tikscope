import { h, card, sectionTitle, kv, toast } from "../dom.js";
import { api } from "../api.js";
import { fmtInt, fmtBytes, fmtDuration, fmtPct, fmtAgo, fmtMs } from "../format.js";

function accountCard(who) {
  const field = (label, name, autocomplete) => h("label", { class: "field" }, h("span", null, label),
    h("input", { name, type: "password", required: true, autocomplete, minlength: name === "current" ? 1 : 12, maxlength: 256 }));
  const error = h("p", { class: "form-error", role: "alert", hidden: true });
  const submit = h("button", { type: "submit", class: "btn primary" }, "Change password");
  const form = h("form", { class: "form", autocomplete: "on",
    onsubmit: async (e) => {
      e.preventDefault();
      const data = Object.fromEntries(new FormData(form).entries());
      error.hidden = true;
      if (data.new !== data.confirm) {
        error.textContent = "The new passwords do not match.";
        error.hidden = false;
        return;
      }
      submit.disabled = true;
      try {
        await api.changePassword(data.current, data.new);
        form.reset();
        toast("Password changed; other sessions were signed out");
      } catch (err) {
        error.textContent = err.message;
        error.hidden = false;
      } finally {
        submit.disabled = false;
      }
    } },
    h("input", { type: "text", name: "username", value: who.username, autocomplete: "username", hidden: true, readonly: true }),
    field("Current password", "current", "current-password"),
    field("New password", "new", "new-password"),
    field("Repeat new password", "confirm", "new-password"),
    error, submit);
  return card(sectionTitle("lock", "amber", "Account", `${who.username}, signed in ${fmtAgo(who.since)}`), form,
    h("p", { class: "form-hint" }, "Passwords are stored as Argon2id hashes. Forgotten it? Run `tikscope set-password <user>` on the server."));
}

export const system = {
  async load(state, signal) {
    const [status, who] = await Promise.all([api.status(signal), api.authStatus(signal)]);
    return { ...status, who };
  },

  render(s) {
    const { ingest, store, router } = s;
    const sources = Object.entries(ingest.routers).sort((a, b) => b[1] - a[1]);
    const collector = card(sectionTitle("server", "blue", "Collector", `tikscope ${s.version}`), kv([
      ["Uptime", fmtDuration(s.uptime_secs)],
      ["DNS retention", fmtDuration(s.retention.queries_secs)],
      ["Metrics retention", fmtDuration(s.retention.metrics_secs)],
      ["Events retention", fmtDuration(s.retention.events_secs)],
      ["Database size", fmtBytes(store.size_bytes)],
      ["Rows written", fmtInt(store.written)],
      ["Dropped (queue full)", fmtInt(store.dropped)],
    ]));
    const stream = card(sectionTitle("radio", "green", "Log stream", "Syslog lines since start"), kv([
      ["Lines received", fmtInt(ingest.received)],
      ["DNS queries", fmtInt(ingest.queries)],
      ["Answered", `${fmtInt(ingest.answered)} (${fmtPct(ingest.answered, ingest.queries, 0)})`],
      ["Answers without a query", fmtInt(ingest.orphan_answers)],
      ["Other events", fmtInt(ingest.events)],
      ["DHCP leases seen", fmtInt(ingest.leases)],
      ["Rejected (not in SYSLOG_SOURCES)", fmtInt(ingest.rejected)],
      ["Waiting for answer", fmtInt(ingest.pending)],
    ]), h("h3", { style: { margin: "14px 0 6px" } }, "Sources"),
      sources.length ? kv(sources.map(([addr, ts]) => [addr, fmtAgo(ts)])) : h("p", { class: "muted" }, "No syslog messages received yet."));

    let routerCard;
    if (!router) {
      routerCard = card(sectionTitle("router", "violet", "Router", "REST API not configured"),
        h("p", { class: "muted" }, "Set MIKROTIK_URL, MIKROTIK_USER and MIKROTIK_PASSWORD to collect interface throughput, system health, connections, DHCP leases, ARP, IPv6 neighbours, Kid Control bandwidth and Wi-Fi signal."));
    } else if (router.error && !router.identity) {
      routerCard = card(sectionTitle("router", "rose", "Router", "Poll failed"), h("p", { class: "mono" }, router.error));
    } else {
      routerCard = card(sectionTitle("router", "violet", "Router", `${router.identity}, polled ${fmtAgo(router.updated_at)}`), kv([
        ["Board", router.board],
        ["RouterOS", router.version],
        ["Uptime", router.uptime],
        ["CPU load", `${router.cpu_load}%`],
        ["Memory used", `${fmtBytes(router.total_memory - router.free_memory)} of ${fmtBytes(router.total_memory)}`],
        router.total_hdd ? ["Storage used", `${fmtBytes(router.total_hdd - router.free_hdd)} of ${fmtBytes(router.total_hdd)}`] : null,
        ...Object.entries(router.health || {}).map(([k, v]) => [k.replace(/-/g, " "), v]),
        ["Connections", router.max_connections ? `${fmtInt(router.connections)} of ${fmtInt(router.max_connections)}` : fmtInt(router.connections)],
        ["WAN interface", router.wan || "not detected"],
        ["DNS servers", [router.dns_servers, router.dns_dynamic_servers].filter(Boolean).join(", ") || "-"],
        ["DNS cache", `${fmtInt(router.cache_used_kib)} of ${fmtInt(router.cache_size_kib)} KiB`],
        ["Known hosts", `${fmtInt(router.hosts)} (${fmtInt(router.leases)} leases)`],
        ["Kid Control", router.kid_control ? "enabled, per-device bandwidth available" : "not enabled"],
        ["Wi-Fi clients", fmtInt((router.wifi_clients || []).length)],
      ].filter(Boolean)),
        router.pools && router.pools.length ? [h("h3", { style: { margin: "14px 0 6px" } }, "DHCP pools"), kv(router.pools.map((p) => [`${p.name} (${p.ranges})`, `${fmtInt(p.used)} of ${fmtInt(p.size)} used, ${p.used_pct.toFixed(0)}%`]))] : null,
        router.adlists && router.adlists.length ? [h("h3", { style: { margin: "14px 0 6px" } }, "Adlists"), kv(router.adlists.map((a) => [a.url.replace(/^https?:\/\//, ""), `${fmtInt(a.matches)} hits, ${fmtInt(a.names)} names`]))] : null,
        router.error ? h("p", { class: "muted mono", style: { marginTop: "10px" } }, `Partial poll: ${router.error}`) : null);
    }

    const probes = card(sectionTitle("zap", "sky", "Probes", `${s.probes.length} configured`),
      s.probes.length ? kv(s.probes.map((p) => [`${p.target.kind} ${p.target.addr}`, `${p.up ? "up" : "down"}, last ${fmtMs(p.last_rtt_ms)}${p.consecutive_failures ? `, ${p.consecutive_failures} failures` : ""}`]))
        : h("p", { class: "muted" }, "Set PROBE_ICMP and PROBE_DNS to monitor reachability and resolver latency."));

    return [h("div", { class: "grid cols-2" }, collector, stream), h("div", { class: "grid cols-2" }, routerCard, probes), h("div", { class: "grid cols-2" }, accountCard(s.who))];
  },
};
