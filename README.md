<p align="center"><img src="web/logo.svg" width="96" alt="tikscope logo"></p>

# tikscope

**Network visibility &middot; Router monitoring.** Monitoring for MikroTik routers in one small Go binary: DNS analytics from
the router's log stream, throughput and health from the REST API,
reachability probes from the collector, and a list of current issues derived
from all three. SQLite storage, embedded web UI, no other services.

## What it shows

- **Issues**: packet loss or latency spikes to probe targets, DNS failure
  rate, slow or missing DNS answers, link flaps and interface errors, high
  CPU, memory or connection table usage, WAN interface down, Netwatch hosts
  down, silent WireGuard peers, DHCP pools almost full, router log errors,
  stalled log stream.
- **DNS**: every query with client, domain, record type, outcome (`ok`,
  `nodata`, `nxdomain`, `servfail`, `blocked`, `error`), answer records and
  response time, from RouterOS 7 `dns query` and `dns done query` log lines.
  Timelines, outcome and type breakdowns, top and problem domains, top
  clients, a filterable query log with CSV export, per client and per domain
  drill downs with a live "resolve now".
- **Network**: download and upload speedometers, per interface graphs,
  interface table with link rate, errors, drops and link downs, CPU, memory
  and connection history, WireGuard peers, Netwatch hosts, ICMP and DNS probe
  latency and loss.
- **Devices**: every known client with name and MAC (DHCP log lines, leases,
  ARP, IPv6 neighbours), DNS activity, per device bandwidth (Kid Control) and
  Wi-Fi signal.
- **Events**: the router log with severity, search and paging.
- **System**: collector counters, router identity, version, uptime, health
  sensors, DHCP pool usage, DNS cache, adlists, probe state.

Time ranges from 1 hour to 30 days. Light and dark themes in Grafana's
colors. Every chart scales to its own peak, the speedometers to the line
speed or the peak in range. Refreshes are seamless. Works on phones, and a
wall mode (`#wall`) fits any screen without scrolling.

## Screenshots

Overview: issues, WAN speedometers, interfaces, reachability, DNS.

![Overview](docs/overview.png)

Network: throughput with peak bands, interfaces, CPU, memory, connections,
WireGuard, Netwatch, probes.

![Network](docs/network.png)

DNS analytics and a device drill-down.

![DNS](docs/dns.png)

![Device](docs/device-detail.png)

System: collector counters, log stream, router snapshot, probes.

![System](docs/system.png)

Wall mode for a TV or a spare monitor: clock, issue strip, fullscreen
button, scales up on large displays.

![Wall](docs/wall.png)

Dark theme:

![Overview, dark](docs/overview-dark.png)

## Setup

Run the collector, point the router's log at it, create a read-only API
user, verify. Replace `192.168.88.1` (router), `192.168.88.50` (collector)
and the subnet with yours.

### 1. Run the collector

Docker Compose:

```bash
git clone https://github.com/aganet/tikscope.git
cd tikscope
cp .env.example .env      # edit MIKROTIK_* and PROBE_* values
docker compose up -d --build
```

Plain Docker, no clone:

```bash
docker build -t tikscope https://github.com/aganet/tikscope.git
docker run -d --name tikscope --restart unless-stopped \
  -p 3000:3000 -p 5354:5354/udp -v $(pwd)/data:/data \
  -e MIKROTIK_URL=https://192.168.88.1 -e MIKROTIK_USER=tikscope \
  -e MIKROTIK_PASSWORD=change-me -e MIKROTIK_INSECURE=true \
  -e PROBE_DNS=192.168.88.1 -e POLL_INTERVAL=1s \
  tikscope
```

Binary, no Docker (Go 1.26+):

```bash
make build
set -a; . ./.env; set +a
./tikscope
```

Dashboard <http://localhost:3000>, syslog UDP 5354, data in `./data`.

### 2. Send the router log to the collector

Terminal:

```text
/system logging action add name=tikscope target=remote remote=192.168.88.50 remote-port=5354
/system logging add topics=dns action=tikscope
/system logging add topics=info,!dns action=tikscope
/system logging add topics=warning action=tikscope
/system logging add topics=error action=tikscope
/system logging add topics=critical action=tikscope
```

Winbox / WebFig:

1. **System > Logging**, tab **Actions**, **+**: Name `tikscope`, Type `remote`, Remote Address `192.168.88.50`, Remote Port `5354`, OK.
2. Tab **Rules**, **+**: Topics `dns`, Action `tikscope`, OK.
3. Repeat for `info` (add a second topic `!dns` with the `-` toggle), `warning`, `error`, `critical`.

Do not add the `packet` sub topic. Default, BSD syslog and ISO timestamp
formats are all accepted.

### 3. Create the read-only API user

Needed for throughput, speedometers, health, connections, device names, Kid
Control bandwidth, Wi-Fi, WireGuard, Netwatch and pools. Without it you still
get DNS, events and probes.

Terminal:

```text
# HTTPS service with a self-signed certificate (skip the certificate lines if www-ssl already has one)
/certificate add name=router-ca common-name=router-ca key-usage=key-cert-sign,crl-sign days-valid=3650
/certificate sign router-ca
/certificate add name=router-ssl common-name=router.lan key-usage=digital-signature,key-encipherment,tls-server days-valid=3650
/certificate sign router-ssl ca=router-ca
/ip service set www-ssl disabled=no certificate=router-ssl
/ip firewall filter add chain=input protocol=tcp dst-port=443 src-address=192.168.88.0/24 action=accept comment="tikscope API" place-before=0

# read-only group and user; api is required because REST logs in through the API
/user group add name=tikscope policy=read,api,rest-api,test,!local,!telnet,!ssh,!ftp,!reboot,!write,!policy,!winbox,!password,!web,!sniff,!sensitive,!romon
/user add name=tikscope group=tikscope password=change-me address=192.168.88.50/32

# optional: per device bandwidth
/ip kid-control device add name=all mac-address=00:00:00:00:00:00 dynamic=yes
```

Each `certificate sign` prints `progress: done`; wait for it.

Winbox / WebFig:

1. **System > Certificates**, **+**: Name `router-ca`, Common Name `router-ca`, tab **Key Usage**: tick only `crl sign` and `key cert. sign`, **Apply**, **Sign**, **Start**, wait for "done", **Close**.
2. **+** again: Name `router-ssl`, Common Name `router.lan`, Key Usage: tick only `digital signature`, `key encipherment`, `tls server`, **Apply**, **Sign**, CA `router-ca`, **Start**, wait, **Close**.
3. **IP > Services**, double-click **www-ssl**: untick Disabled, Certificate `router-ssl`, OK.
4. **IP > Firewall**, tab **Filter Rules**, **+**: Chain `input`, Protocol `tcp`, Dst. Port `443`, Src. Address `192.168.88.0/24`, tab **Action** `accept`, OK, drag the rule to the top.
5. **System > Users**, tab **Groups**, **+**: Name `tikscope`, tick only **read**, **api**, **rest-api**, **test**, OK.
6. Tab **Users**, **+**: Name `tikscope`, Group `tikscope`, Allowed Address `192.168.88.50/32`, Password, OK.
7. Optional, per device bandwidth: **IP > Kid Control**, tab **Devices**, **+**: Name `all`, MAC Address `00:00:00:00:00:00`, tick **Dynamic**, OK.

### 4. Verify

From the collector host:

```bash
curl -k -u tikscope:change-me https://192.168.88.1/rest/system/identity
```

must print `{"name":"..."}`. `401`: wrong password, missing `rest-api` or
the address restriction. `500 not allowed`: the group lacks `api`. TLS
handshake error: `www-ssl` has no certificate.

Open the dashboard. The first visit creates the account: username, a
password of at least 12 characters, and the setup token from the collector
log:

```bash
docker compose logs tikscope | grep setup_token
```

The header shows the router name within 15 s, the System tab the collector
counters and router snapshot, the DNS tab fills with the first query line.

Forgot the password? Set a new one from the server (works while the
collector runs, signs that user out everywhere):

```bash
echo -n 'a new long passphrase' | docker compose exec -T tikscope tikscope set-password admin
```

## Configuration

Environment variables; with Compose, everything in `.env` is passed through.

| Variable | Default | Description |
| --- | --- | --- |
| `HTTP_ADDR` | `:3000` | Dashboard and API listen address |
| `SYSLOG_ADDR` | `:5354` | UDP syslog listen address |
| `SYSLOG_SOURCES` | any | Comma separated addresses or CIDRs allowed to send syslog, e.g. `192.168.88.1` |
| `SETUP_TOKEN` | random, printed in the log | Token the account creation page asks for while no account exists |
| `DATABASE_PATH` | `./data/tikscope.db` (`/data/tikscope.db` in Docker) | SQLite file |
| `RETENTION` | `7d` | How long DNS queries are kept |
| `METRICS_RETENTION` | `30d` | How long samples are kept |
| `EVENTS_RETENTION` | `30d` | How long router log events are kept |
| `MIKROTIK_URL`, `MIKROTIK_USER`, `MIKROTIK_PASSWORD` | unset | RouterOS REST API |
| `MIKROTIK_INSECURE` | `false` | Skip TLS certificate verification |
| `POLL_INTERVAL` | `5s` | Fast poll: interfaces, CPU, memory, connections (1s minimum) |
| `POLL_SLOW_INTERVAL` | `1m` | Slow poll: leases, ARP, Wi-Fi, WireGuard, Netwatch, pools, link rates |
| `METRIC_INTERVAL` | `15s` | How often fast metrics are written to the database |
| `WAN_INTERFACE` | auto (default route) | Interface featured on the overview |
| `WAN_DOWN_MBPS`, `WAN_UP_MBPS` | auto (peak in range) | Line speed used as the speedometer scale |
| `PROBE_ICMP` | `1.1.1.1,8.8.8.8` | ICMP targets, comma separated, `none` to disable |
| `PROBE_DNS` | `DNS_SERVER`, else the router | Resolvers to query, comma separated |
| `PROBE_DNS_NAME` | `example.com` | Name resolved by the DNS probes |
| `PROBE_INTERVAL` | `10s` | Probe interval |
| `DNS_SERVER` | system resolver | Resolver for "resolve now" |
| `LOG_LEVEL` | `info` | `debug` logs every request |
| `TZ` | `UTC` | Time zone for logs and CSV exports |

## Security

- Login required. The first visitor creates the account with the setup token
  from the log; after that registration is closed and accounts are only
  added with `tikscope set-password`.
- Passwords are hashed with Argon2id (64 MiB, 3 passes, 4 lanes, random
  16-byte salt); hashes record their parameters, so stronger settings in a
  future version apply on the next login.
- Sessions are 32 random bytes in an `HttpOnly`, `SameSite=Strict` cookie,
  `Secure` over TLS or behind an `X-Forwarded-Proto: https` proxy. Only the
  SHA-256 of the token is stored. Sessions last 30 days, expire after 7 idle
  days, and are revoked on logout and password change.
- Login is throttled per client address and per username: after 5 failures
  the next attempt waits 30 s, doubling up to 1 h. Unknown usernames take as
  long to reject as wrong passwords.
- State-changing requests must come from the same origin with a JSON body;
  with the strict cookie that covers CSRF. Strict Content Security Policy
  (no inline or remote scripts), `X-Frame-Options: DENY`, `nosniff`.
- The login travels in clear text on plain HTTP; outside a trusted LAN put
  the dashboard behind a TLS reverse proxy.
- Set `SYSLOG_SOURCES` to the router address; UDP has no sender
  authentication.
- The API user is read-only and pinned to the collector's address (step 3).
  `MIKROTIK_INSECURE=true` disables certificate checks for the router's
  self-signed certificate; install a real one to turn it off.
- The compose file drops every capability except `NET_RAW` (ICMP probes)
  and sets `no-new-privileges`. Without `NET_RAW` the probes fall back to
  unprivileged ICMP sockets, which need `net.ipv4.ping_group_range` to
  include the process group.
- `make audit` runs `govulncheck` and `trivy`.

## API

Every endpoint except `/healthz` and `/api/auth/*` needs the session cookie
and takes `range` (`1h`, `6h`, `24h`, `7d`, `30d`; default `24h`).

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
| `GET /healthz` | liveness, no login needed |
| `GET /api/auth/status` | whether an account exists and whether the caller is signed in |
| `POST /api/auth/setup` | create the first account (`username`, `password`, `token`) |
| `POST /api/auth/login`, `POST /api/auth/logout` | start and end a session |
| `POST /api/auth/password` | change the password (`current`, `new`) |

Metrics: `iface.rx_bps`, `iface.tx_bps`, `iface.errors`, `iface.drops`,
`iface.link_downs`, `dev.rx_bps`, `dev.tx_bps`, `wg.rx_bps`, `wg.tx_bps`,
`sys.cpu`, `sys.mem_used_pct`, `sys.connections`, `sys.temperature`,
`sys.voltage`, `health.<sensor>`, `pool.used_pct`, `wifi.signal`,
`icmp.rtt_ms`, `icmp.loss`, `dnsprobe.rtt_ms`, `dnsprobe.loss`.

## Development

```bash
make test     # go vet, go test -race, JS syntax check
make build    # static binary with the UI embedded
./tikscope    # http://localhost:3000, syslog on :5354, data in ./data
./tikscope set-password admin   # new password on stdin; creates the user if needed
./tikscope version
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
