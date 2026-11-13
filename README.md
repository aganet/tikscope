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

Time ranges from 1 hour to 30 days, light and dark themes, works on phones,
and a wall mode that fits any screen. Every chart scales to its own peak,
the speedometers to the configured line speed or the peak in range, and the
"no issues" banner can be dismissed.

## Screenshots

Overview: issues, speedometers for the WAN, per interface graphs, reachability, DNS.

![Overview](docs/overview.png)

Network: throughput with peak bands, interfaces with link rate, errors and
drops, CPU, memory and connections, WireGuard peers, Netwatch, probes.

![Network](docs/network.png)

DNS analytics and the device drill-down with bandwidth and Wi-Fi signal.

![DNS](docs/dns.png)

![Device](docs/device-detail.png)

System: collector counters, log stream, router snapshot, probes.

![System](docs/system.png)

Wall mode (`#wall`): edge to edge for a TV or a spare monitor, with a clock,
issue strip and fullscreen button. It fits the screen without scrolling and
scales up on large displays.

![Wall](docs/wall.png)

Dark theme:

![Overview, dark](docs/overview-dark.png)

## Setup

Four steps: run the collector, point the router's log at it, create a
read-only API user, verify. Replace `192.168.88.1` (router), `192.168.88.50`
(collector host) and the subnet with yours.

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

Binary, no Docker (Go 1.25+ only):

```bash
make build
set -a; . ./.env; set +a
./tikscope
```

Dashboard: <http://localhost:3000>. Syslog: UDP 5354. Data: `./data`.

### 2. Send the router log to the collector

Command line (Winbox or WebFig terminal, or SSH):

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
3. Repeat for `info` (add a second topic `!dns` with the small `-` toggle), `warning`, `error`, `critical`.

Do not add the `packet` sub topic. Default, BSD syslog and ISO timestamp
formats are all accepted.

### 3. Create the read-only API user

Needed for throughput, speedometers, system health, connections, device
names, Kid Control bandwidth, Wi-Fi, WireGuard, Netwatch and pools. Skip it
and you still get DNS, events and probes.

Command line:

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
2. **+** again: Name `router-ssl`, Common Name `router.lan`, Key Usage: tick only `digital signature`, `key encipherment`, `tls server`, **Apply**, **Sign**, choose CA `router-ca`, **Start**, wait, **Close**.
3. **IP > Services**, double-click **www-ssl**: untick Disabled, Certificate `router-ssl`, OK.
4. **IP > Firewall**, tab **Filter Rules**, **+**: Chain `input`, Protocol `tcp`, Dst. Port `443`, Src. Address `192.168.88.0/24`, tab **Action** `accept`, OK, then drag the rule to the top.
5. **System > Users**, tab **Groups**, **+**: Name `tikscope`, tick only **read**, **api**, **rest-api**, **test**, OK.
6. Tab **Users**, **+**: Name `tikscope`, Group `tikscope`, Allowed Address `192.168.88.50/32`, Password, OK.
7. Optional, per device bandwidth: **IP > Kid Control**, tab **Devices**, **+**: Name `all`, MAC Address `00:00:00:00:00:00`, tick **Dynamic**, OK.

### 4. Verify

From the collector host:

```bash
curl -k -u tikscope:change-me https://192.168.88.1/rest/system/identity
```

must print `{"name":"..."}`. `401` means wrong password, missing `rest-api`
or the user's address restriction; `500 not allowed` means the group lacks
`api`; a TLS handshake error means `www-ssl` has no certificate.

Then open the dashboard. On the first visit it asks you to create the
account: pick a username, a password of at least 12 characters, and paste
the setup token from the collector log:

```bash
docker compose logs tikscope | grep setup_token
```

After that the header shows the router name within 15 s, the System tab
shows the collector counters and the router snapshot, and the DNS tab fills
as soon as the router sends its first query line.

Forgot the password? Set a new one from the server (works while the
collector is running, signs out that user everywhere):

```bash
echo -n 'a new long passphrase' | docker compose exec -T tikscope tikscope set-password admin
```

## Configuration

| Variable | Default | Description |
| --- | --- | --- |
| `HTTP_ADDR` | `:3000` | Dashboard and API listen address |
| `SYSLOG_ADDR` | `:5354` | UDP syslog listen address |
| `SYSLOG_SOURCES` | any | Comma separated addresses or CIDRs allowed to send syslog, e.g. `192.168.88.1` |
| `SETUP_TOKEN` | random, printed in the log | Token required by the account creation page while no account exists |
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
| `PROBE_DNS` | router address when known | Resolvers to query, comma separated |
| `PROBE_DNS_NAME` | `example.com` | Name resolved by the DNS probes |
| `PROBE_INTERVAL` | `10s` | Probe interval |
| `DNS_SERVER` | system resolver | Resolver for "resolve now" |
| `LOG_LEVEL` | `info` | `debug` logs every request |
| `TZ` | `UTC` | Time zone for logs and CSV exports |

## Security notes

- The dashboard requires a login. The first visitor creates the account
  with the setup token from the log; after that registration is closed and
  further accounts are only added with `tikscope set-password`.
- Passwords are hashed with Argon2id (64 MiB, 3 passes, 4 lanes, random
  16-byte salt). Hashes record their parameters, so stronger settings in a
  future version are applied on the next login.
- Sessions are 32 random bytes in an `HttpOnly`, `SameSite=Strict` cookie
  (`Secure` when served over TLS or behind an `X-Forwarded-Proto: https`
  proxy). Only the SHA-256 of the token is stored, so a copy of the database
  does not grant access. Sessions last 30 days, expire after 7 idle days and
  are revoked on logout and on password change.
- Login is throttled per client address and per username: after 5 failures
  the next attempt waits 30 s, doubling up to 1 h. Unknown usernames take as
  long to reject as wrong passwords.
- State-changing requests must come from the same origin (fetch metadata and
  `Origin` are checked) with a JSON body, which browsers cannot send
  cross-site without CORS; combined with the strict cookie that covers CSRF.
- Pages are served with a strict Content Security Policy (no inline or
  remote scripts), `X-Frame-Options: DENY` and `nosniff`.
- The login still travels in clear text on plain HTTP. On anything but a
  trusted LAN put the dashboard behind a TLS reverse proxy.
- Set `SYSLOG_SOURCES` to the router address so forged syslog from other
  hosts is dropped (UDP has no sender authentication).
- The API user is read-only and should be pinned to the collector's address
  (step 3 above). `MIKROTIK_INSECURE=true` disables certificate checks for
  the router's self-signed certificate; install a real certificate on the
  router to turn it off.
- The compose file drops all Linux capabilities except `NET_RAW` (ICMP
  probes) and `NET_BIND_SERVICE`, and sets `no-new-privileges`.
- `govulncheck` and `trivy` run clean at release time; `make audit` runs both.

ICMP probes use a raw socket when running as root or with `CAP_NET_RAW`
(the default in the container) and fall back to the unprivileged datagram
socket, which requires `net.ipv4.ping_group_range` to include the process
group.

## API

All endpoints except `/healthz` and `/api/auth/*` need the session cookie
and take `range` (`1h`, `6h`, `24h`, `7d`, `30d`; default `24h`).

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
./tikscope set-password admin   # new password on stdin; creates the user if needed
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
