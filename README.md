# tikscope

A monitoring dashboard for MikroTik routers in one small Go binary: DNS
analytics from the router's log stream, throughput and health from the REST
API, reachability probes from the collector, and a list of current issues
derived from all three. SQLite storage, embedded web UI, no other services.

## What it shows

- **Issues**: packet loss or latency spikes to probe targets, DNS failure rate,
  slow or missing DNS answers, link flaps and interface errors, high CPU,
  memory or connection table usage, WAN interface down, Netwatch hosts down,
  silent WireGuard peers, DHCP pools almost full, router log errors, and a
  stalled log stream.
- **DNS**: every query with client, domain, record type, outcome (`ok`,
  `nodata`, `nxdomain`, `servfail`, `blocked`, `error`), the answer records
  and the response time, taken from RouterOS 7 `dns query` and `dns done
  query` log lines. Timelines, outcome and type breakdowns, top and problem
  domains, top clients, a filterable query log with CSV export, and per
  client and per domain drill downs with a live "resolve now".
- **Network**: download and upload speedometers and per interface graphs,
  interface table with negotiated link rate, errors, drops and link downs,
  CPU, memory and connection history, WireGuard peers with handshake age and
  traffic, Netwatch hosts, ICMP and DNS probe latency and loss.
- **Devices**: every known client with name and MAC (DHCP log lines, DHCP
  leases, ARP, IPv6 neighbours), DNS activity, per device bandwidth (Kid
  Control) and Wi-Fi signal.
- **Events**: the router log with severity, search and paging.
- **System**: collector counters, router identity, version, uptime, every
  health sensor, DHCP pool usage, DNS cache, adlists, probe state.

Time ranges from 1 hour to 30 days, light and dark themes, works on phones.

## Screenshots

Overview: issues, speedometers for the WAN, per interface graphs, reachability, DNS.

![Overview](docs/overview.png)

Network: throughput with peak bands, interfaces with link rate, errors and
drops, CPU, memory and connections, WireGuard peers, Netwatch, probes.

![Network](docs/network.png)

DNS analytics and the device drill-down with bandwidth and Wi-Fi signal.

![DNS](docs/dns.png)

![Device](docs/device-detail.png)

Dark theme:

![Overview, dark](docs/overview-dark.png)

## Quick start

```bash
git clone https://github.com/aganet/tikscope.git
cd tikscope
cp .env.example .env    # router credentials, probes, line speed
docker compose up -d --build
```

The dashboard is on <http://localhost:3000>; syslog is received on UDP 5354.

Without Docker: `make build && ./tikscope` (needs Go only, no C toolchain).

## Router configuration

Send the DNS log topic, and the rest of the log for events, to the collector:

```text
/system logging action add name=tikscope target=remote remote=<collector-ip> remote-port=5354
/system logging add topics=dns action=tikscope
/system logging add topics=info,!dns action=tikscope
/system logging add topics=warning action=tikscope
/system logging add topics=error action=tikscope
/system logging add topics=critical action=tikscope
```

Default, BSD syslog and ISO timestamp formats are all accepted. Do not add the
`packet` sub topic.

For throughput, health and device names create a read-only API user:

```text
/user group add name=tikscope policy=read,api,rest-api,test,!local,!telnet,!ssh,!ftp,!reboot,!write,!policy,!winbox,!password,!web,!sniff,!sensitive,!romon
/user add name=tikscope group=tikscope password=<password>
```

REST logs in through the binary API on the user's behalf, so `api` is
required next to `rest-api` (without it every call fails with
`not allowed (9)`); `test` covers monitoring reads.

Set `MIKROTIK_URL=https://<router-ip>`, `MIKROTIK_USER`,
`MIKROTIK_PASSWORD` (plus `MIKROTIK_INSECURE=true` with the default
self-signed certificate).

Per device bandwidth needs Kid Control, which is free on every RouterOS 7
license and tracks all LAN devices once one dynamic entry exists:

```text
/ip kid-control device add name=all mac-address=00:00:00:00:00:00 dynamic=yes
```

## Configuration

| Variable | Default | Description |
| --- | --- | --- |
| `HTTP_ADDR` | `:3000` | Dashboard and API listen address |
| `SYSLOG_ADDR` | `:5354` | UDP syslog listen address |
| `DATABASE_PATH` | `./data/tikscope.db` (`/data/tikscope.db` in Docker) | SQLite file |
| `RETENTION` | `7d` | How long DNS queries are kept |
| `METRICS_RETENTION` | `30d` | How long samples are kept |
| `EVENTS_RETENTION` | `30d` | How long router log events are kept |
| `MIKROTIK_URL`, `MIKROTIK_USER`, `MIKROTIK_PASSWORD` | unset | RouterOS REST API |
| `MIKROTIK_INSECURE` | `false` | Skip TLS certificate verification |
| `POLL_INTERVAL` | `15s` | REST API poll interval |
| `WAN_INTERFACE` | auto (default route) | Interface featured on the overview |
| `WAN_DOWN_MBPS`, `WAN_UP_MBPS` | auto (peak in range) | Line speed used as the speedometer scale |
| `PROBE_ICMP` | `1.1.1.1,8.8.8.8` | ICMP targets, comma separated, `none` to disable |
| `PROBE_DNS` | router address when known | Resolvers to query, comma separated |
| `PROBE_DNS_NAME` | `example.com` | Name resolved by the DNS probes |
| `PROBE_INTERVAL` | `10s` | Probe interval |
| `DNS_SERVER` | system resolver | Resolver for "resolve now" |
| `LOG_LEVEL` | `info` | `debug` logs every request |
| `TZ` | `UTC` | Time zone for logs and CSV exports |

ICMP probes use a raw socket when running as root or with `CAP_NET_RAW`
(the default in the container) and fall back to the unprivileged datagram
socket, which requires `net.ipv4.ping_group_range` to include the process
group.

## API

All endpoints take `range` (`1h`, `6h`, `24h`, `7d`, `30d`; default `24h`).

| Endpoint | Returns |
| --- | --- |
| `GET /api/dns/summary?limit=` | totals, outcomes, types, timeline, top and problem domains, top clients |
| `GET /api/dns/timeline?client=&domain=` | per bucket counts by outcome and average response time |
| `GET /api/dns/domains?search=&outcome=&client=&limit=` | domains with counts, clients, problems, last seen |
| `GET /api/dns/clients?domain=&limit=` | clients with device name, MAC, counts |
| `GET /api/dns/queries?client=&domain=&search=&type=&outcome=&page=&page_size=` | paged query rows and total |
| `GET /api/dns/export.csv?...` | the same rows as CSV |
| `GET /api/dns/resolve?name=&type=` | live lookup |
| `GET /api/metrics?metric=&key=` | bucketed avg, max, min per series |
| `GET /api/network?interface=` | router snapshot, interfaces, throughput, system and probe timelines |
| `GET /api/devices` | known devices with DNS, bandwidth and Wi-Fi data |
| `GET /api/events?severity=&search=&page=&page_size=` | router log |
| `GET /api/issues` | current issues |
| `GET /api/status` | collector counters, router snapshot, probe state |
| `GET /healthz` | liveness |

Metric names: `iface.rx_bps`, `iface.tx_bps`, `iface.errors`,
`iface.link_downs`, `dev.rx_bps`, `dev.tx_bps`, `sys.cpu`,
`sys.mem_used_pct`, `sys.connections`, `sys.temperature`, `sys.voltage`,
`wifi.signal`, `icmp.rtt_ms`, `icmp.loss`, `dnsprobe.rtt_ms`,
`dnsprobe.loss`.

## Development

```bash
make test     # go vet, go test -race, JS syntax check
make build    # static binary with the UI embedded
./tikscope    # http://localhost:3000, syslog on :5354, data in ./data
```

Feed test lines without a router:

```bash
printf 'dns query from 192.168.88.10: #1 example.com. A\ndns done query: #1 example.com 93.184.216.34\n' | nc -u -w1 127.0.0.1 5354
```

The web UI is plain HTML, CSS and ES modules under `web/`, embedded at build
time; edit and rebuild, there is no bundler.

## License

MIT, see [LICENSE](LICENSE). Inspired by
[publi0/mikrotik-dns](https://github.com/publi0/mikrotik-dns); this is an
independent implementation.
