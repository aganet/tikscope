// Package api serves the JSON API and the embedded dashboard.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/aganet/tikscope/internal/auth"
	"github.com/aganet/tikscope/internal/ingest"
	"github.com/aganet/tikscope/internal/issues"
	"github.com/aganet/tikscope/internal/probe"
	"github.com/aganet/tikscope/internal/routeros"
	"github.com/aganet/tikscope/internal/store"
)

const (
	defaultRange = 24 * time.Hour
	maxRange     = 365 * 24 * time.Hour
	maxLimit     = 500
	maxPageSize  = 500
	cacheTTL     = time.Second
)

// Deps are the runtime components the handlers read from.
type Deps struct {
	Store     *store.Store
	Auth      *auth.Service
	Ingest    *ingest.Ingester
	Poller    *routeros.Poller // nil when the REST API is not configured
	Prober    *probe.Prober
	Web       fs.FS
	Resolver  *net.Resolver
	Version   string
	Retention store.Retention
	Log       *slog.Logger
	// Configured WAN line speeds in bits per second; zero means auto-scale.
	WANDownBps float64
	WANUpBps   float64
}

type API struct {
	Deps
	started time.Time
	cache   *responseCache
}

func New(d Deps) *API {
	return &API{Deps: d, started: time.Now(), cache: newResponseCache(cacheTTL)}
}

func (a *API) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/dns/summary", a.cached(a.dnsSummary))
	mux.HandleFunc("GET /api/dns/timeline", a.dnsTimeline)
	mux.HandleFunc("GET /api/dns/domains", a.dnsDomains)
	mux.HandleFunc("GET /api/dns/clients", a.dnsClients)
	mux.HandleFunc("GET /api/dns/queries", a.dnsQueries)
	mux.HandleFunc("GET /api/dns/export.csv", a.dnsExport)
	mux.HandleFunc("GET /api/dns/resolve", a.dnsResolve)
	mux.HandleFunc("GET /api/metrics", a.metrics)
	mux.HandleFunc("GET /api/network", a.cached(a.network))
	mux.HandleFunc("GET /api/devices", a.cached(a.devices))
	mux.HandleFunc("GET /api/events", a.events)
	mux.HandleFunc("GET /api/issues", a.cached(a.issues))
	mux.HandleFunc("GET /api/status", a.status)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte("ok")) })
	mux.HandleFunc("/api/", func(w http.ResponseWriter, _ *http.Request) { httpError(w, "not found", http.StatusNotFound) })
	a.Auth.Routes(mux)
	mux.Handle("/", staticHandler(a.Web))
	return a.logRequests(secureHeaders(a.Auth.Protect(mux)))
}

func (a *API) logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		if strings.HasPrefix(r.URL.Path, "/api/") {
			a.Log.Debug("request", "path", r.URL.RequestURI(), "took", time.Since(start))
		}
	})
}

// secureHeaders locks the pages to their own origin: no inline or remote
// scripts, no framing, no API caching.
func secureHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", "default-src 'none'; script-src 'self'; style-src 'self'; img-src 'self' data:; connect-src 'self'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "same-origin")
		if strings.HasPrefix(r.URL.Path, "/api/") {
			h.Set("Cache-Control", "no-store")
		}
		next.ServeHTTP(w, r)
	})
}

// staticHandler serves the embedded dashboard. Files are served directly
// rather than through http.FileServer, whose index.html redirect would loop.
func staticHandler(web fs.FS) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(r.URL.Path, "/")
		switch name {
		case "":
			name = "index.html"
		case "login":
			name = "login.html"
		}
		f, err := web.Open(name)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		defer f.Close()
		info, err := f.Stat()
		if err != nil || info.IsDir() {
			http.NotFound(w, r)
			return
		}
		if strings.HasSuffix(name, ".html") {
			w.Header().Set("Cache-Control", "no-cache")
		} else {
			w.Header().Set("Cache-Control", "public, max-age=3600")
		}
		if rs, ok := f.(io.ReadSeeker); ok {
			http.ServeContent(w, r, name, info.ModTime(), rs)
			return
		}
		io.Copy(w, f)
	})
}

// responseCache remembers JSON bodies per request URI for a short time so
// several browsers polling the same view share one computation.
type responseCache struct {
	ttl     time.Duration
	mu      sync.Mutex
	entries map[string]cacheEntry
}

type cacheEntry struct {
	at   time.Time
	body []byte
}

func newResponseCache(ttl time.Duration) *responseCache {
	return &responseCache{ttl: ttl, entries: map[string]cacheEntry{}}
}

// cached wraps a handler that returns a JSON-encodable value or an error.
func (a *API) cached(fn func(r *http.Request) (any, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		key := r.URL.RequestURI()
		a.cache.mu.Lock()
		defer a.cache.mu.Unlock()
		if e, ok := a.cache.entries[key]; ok && time.Since(e.at) < a.cache.ttl {
			writeRaw(w, e.body)
			return
		}
		v, err := fn(r)
		if err != nil {
			a.fail(w, err)
			return
		}
		body, err := json.Marshal(v)
		if err != nil {
			a.fail(w, err)
			return
		}
		a.cache.entries[key] = cacheEntry{at: time.Now(), body: body}
		writeRaw(w, body)
	}
}

type badRequest struct{ msg string }

func (e badRequest) Error() string { return e.msg }

func (a *API) fail(w http.ResponseWriter, err error) {
	var br badRequest
	switch {
	case errors.As(err, &br):
		httpError(w, br.msg, http.StatusBadRequest)
	case errors.Is(err, context.Canceled):
	default:
		a.Log.Error("request failed", "err", err)
		httpError(w, "internal error", http.StatusInternalServerError)
	}
}

// --- DNS ---------------------------------------------------------------

func (a *API) dnsSummary(r *http.Request) (any, error) {
	window, err := rangeParam(r)
	if err != nil {
		return nil, err
	}
	return a.Store.DNSSummary(r.Context(), window, intParam(r, "limit", 10, maxLimit))
}

func (a *API) dnsTimeline(w http.ResponseWriter, r *http.Request) {
	f, window, err := dnsFilter(r)
	if err != nil {
		a.fail(w, err)
		return
	}
	bucket := store.BucketFor(window)
	buckets, err := a.Store.DNSTimeline(r.Context(), f, bucket, time.Now())
	if err != nil {
		a.fail(w, err)
		return
	}
	writeJSON(w, map[string]any{"bucket_secs": bucket, "since": f.Since.Unix(), "buckets": buckets})
}

func (a *API) dnsDomains(w http.ResponseWriter, r *http.Request) {
	f, _, err := dnsFilter(r)
	if err != nil {
		a.fail(w, err)
		return
	}
	out, err := a.Store.Domains(r.Context(), f, intParam(r, "limit", 50, maxLimit))
	if err != nil {
		a.fail(w, err)
		return
	}
	writeJSON(w, out)
}

func (a *API) dnsClients(w http.ResponseWriter, r *http.Request) {
	f, _, err := dnsFilter(r)
	if err != nil {
		a.fail(w, err)
		return
	}
	out, err := a.Store.Clients(r.Context(), f, intParam(r, "limit", 50, maxLimit))
	if err != nil {
		a.fail(w, err)
		return
	}
	writeJSON(w, out)
}

func (a *API) dnsQueries(w http.ResponseWriter, r *http.Request) {
	f, _, err := dnsFilter(r)
	if err != nil {
		a.fail(w, err)
		return
	}
	page := intParam(r, "page", 1, 1<<30)
	size := intParam(r, "page_size", 50, maxPageSize)
	items, total, err := a.Store.Queries(r.Context(), f, page, size)
	if err != nil {
		a.fail(w, err)
		return
	}
	writeJSON(w, map[string]any{"items": items, "total": total, "page": page, "page_size": size})
}

func (a *API) dnsExport(w http.ResponseWriter, r *http.Request) {
	f, _, err := dnsFilter(r)
	if err != nil {
		a.fail(w, err)
		return
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="dns-queries-`+time.Now().Format("20060102-150405")+`.csv"`)
	if err := a.Store.ExportCSV(r.Context(), f, w); err != nil && !errors.Is(err, context.Canceled) {
		a.Log.Error("export", "err", err)
	}
}

type resolution struct {
	Outcome    string   `json:"outcome"`
	Records    []string `json:"records"`
	Error      string   `json:"error,omitempty"`
	DurationMS int64    `json:"duration_ms"`
}

// dnsResolve performs a live lookup so a logged domain can be checked
// against the resolver's current answer.
func (a *API) dnsResolve(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimSuffix(strings.TrimSpace(r.URL.Query().Get("name")), ".")
	if name == "" {
		httpError(w, "name is required", http.StatusBadRequest)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	start := time.Now()
	var records []string
	var err error
	switch strings.ToUpper(r.URL.Query().Get("type")) {
	case "AAAA":
		var ips []net.IPAddr
		ips, err = a.Resolver.LookupIPAddr(ctx, name)
		for _, ip := range ips {
			if ip.IP.To4() == nil {
				records = append(records, ip.IP.String())
			}
		}
	case "CNAME":
		var cname string
		cname, err = a.Resolver.LookupCNAME(ctx, name)
		if c := strings.TrimSuffix(cname, "."); c != "" && c != name {
			records = append(records, c)
		}
	case "MX":
		var mxs []*net.MX
		mxs, err = a.Resolver.LookupMX(ctx, name)
		for _, mx := range mxs {
			records = append(records, strconv.Itoa(int(mx.Pref))+" "+strings.TrimSuffix(mx.Host, "."))
		}
	case "TXT":
		records, err = a.Resolver.LookupTXT(ctx, name)
	default:
		var ips []net.IPAddr
		ips, err = a.Resolver.LookupIPAddr(ctx, name)
		for _, ip := range ips {
			if ip.IP.To4() != nil {
				records = append(records, ip.IP.String())
			}
		}
	}
	res := resolution{Records: records, DurationMS: time.Since(start).Milliseconds(), Outcome: "ok"}
	if res.Records == nil {
		res.Records = []string{}
	}
	if err != nil {
		var dnsErr *net.DNSError
		if errors.As(err, &dnsErr) && dnsErr.IsNotFound {
			res.Outcome = "nxdomain"
		} else {
			res.Outcome = "error"
		}
		res.Error = err.Error()
	} else {
		for _, rec := range records {
			if rec == "0.0.0.0" || rec == "::" {
				res.Outcome = "blocked"
			}
		}
	}
	writeJSON(w, res)
}

// --- Metrics and network --------------------------------------------------

func (a *API) metrics(w http.ResponseWriter, r *http.Request) {
	window, err := rangeParam(r)
	if err != nil {
		a.fail(w, err)
		return
	}
	metric := r.URL.Query().Get("metric")
	if metric == "" {
		httpError(w, "metric is required", http.StatusBadRequest)
		return
	}
	now := time.Now()
	bucket := metricBucket(window)
	series, err := a.Store.MetricTimeline(r.Context(), metric, r.URL.Query().Get("key"), now.Add(-window), bucket, now)
	if err != nil {
		a.fail(w, err)
		return
	}
	writeJSON(w, map[string]any{"bucket_secs": bucket, "since": now.Add(-window).Unix(), "series": series})
}

// metricBucket keeps metric timelines around 200 to 300 points.
func metricBucket(window time.Duration) int64 {
	switch {
	case window <= time.Hour:
		return 15
	case window <= 6*time.Hour:
		return 60
	default:
		return store.BucketFor(window)
	}
}

type probeView struct {
	probe.State
	RTTAvgMS *float64             `json:"rtt_avg_ms"`
	RTTMaxMS *float64             `json:"rtt_max_ms"`
	LossPct  *float64             `json:"loss_pct"`
	RTT      []store.MetricBucket `json:"rtt"`
	Loss     []store.MetricBucket `json:"loss"`
}

type networkView struct {
	BucketSecs int64                                      `json:"bucket_secs"`
	Since      int64                                      `json:"since"`
	Router     *routeros.Snapshot                         `json:"router"`
	WAN        string                                     `json:"wan"`
	Interfaces []routeros.Interface                       `json:"interfaces"`
	Throughput map[string][]store.MetricBucket            `json:"throughput"` // rx, tx for the selected interface
	All        map[string]map[string][]store.MetricBucket `json:"all"`        // rx, tx per interface
	Limits     map[string]float64                         `json:"limits"`     // configured down/up bps, 0 = auto
	System     map[string][]store.MetricBucket            `json:"system"`     // cpu, mem, connections
	Probes     []probeView                                `json:"probes"`
}

func (a *API) network(r *http.Request) (any, error) {
	window, err := rangeParam(r)
	if err != nil {
		return nil, err
	}
	ctx := r.Context()
	now := time.Now()
	since := now.Add(-window)
	bucket := metricBucket(window)
	v := networkView{BucketSecs: bucket, Since: since.Unix(), Throughput: map[string][]store.MetricBucket{}, All: map[string]map[string][]store.MetricBucket{},
		Limits: map[string]float64{"down": a.WANDownBps, "up": a.WANUpBps}, System: map[string][]store.MetricBucket{}, Probes: []probeView{}}
	if a.Poller != nil {
		v.Router = a.Poller.Snapshot()
	}
	if v.Router != nil {
		v.WAN = v.Router.WAN
		v.Interfaces = v.Router.Interfaces
	}
	if v.Interfaces == nil {
		v.Interfaces = []routeros.Interface{}
	}
	iface := r.URL.Query().Get("interface")
	if iface == "" {
		iface = v.WAN
	}
	if iface != "" {
		for metric, name := range map[string]string{routeros.MetricIfaceRx: "rx", routeros.MetricIfaceTx: "tx"} {
			s, err := a.Store.MetricTimeline(ctx, metric, iface, since, bucket, now)
			if err != nil {
				return nil, err
			}
			if len(s) > 0 {
				v.Throughput[name] = s[0].Buckets
			}
		}
	}
	// One query per direction covers every interface at once.
	for metric, name := range map[string]string{routeros.MetricIfaceRx: "rx", routeros.MetricIfaceTx: "tx"} {
		series, err := a.Store.MetricTimeline(ctx, metric, "", since, bucket, now)
		if err != nil {
			return nil, err
		}
		for _, s := range series {
			if v.All[s.Key] == nil {
				v.All[s.Key] = map[string][]store.MetricBucket{}
			}
			v.All[s.Key][name] = s.Buckets
		}
	}
	for metric, name := range map[string]string{routeros.MetricCPU: "cpu", routeros.MetricMemUsed: "mem", routeros.MetricConnections: "connections"} {
		s, err := a.Store.MetricTimeline(ctx, metric, "router", since, bucket, now)
		if err != nil {
			return nil, err
		}
		if len(s) > 0 {
			v.System[name] = s[0].Buckets
		}
	}
	if a.Prober != nil {
		for _, st := range a.Prober.States() {
			rttMetric, lossMetric := probe.MetricICMPRTT, probe.MetricICMPLoss
			if st.Target.Kind == "dns" {
				rttMetric, lossMetric = probe.MetricDNSRTT, probe.MetricDNSLoss
			}
			pv := probeView{State: st, RTT: []store.MetricBucket{}, Loss: []store.MetricBucket{}}
			if agg, err := a.Store.MetricAggregate(ctx, rttMetric, st.Target.Addr, since); err == nil {
				pv.RTTAvgMS, pv.RTTMaxMS = agg.Avg, agg.Max
			}
			if agg, err := a.Store.MetricAggregate(ctx, lossMetric, st.Target.Addr, since); err == nil && agg.Avg != nil {
				pct := *agg.Avg * 100
				pv.LossPct = &pct
			}
			if s, err := a.Store.MetricTimeline(ctx, rttMetric, st.Target.Addr, since, bucket, now); err == nil && len(s) > 0 {
				pv.RTT = s[0].Buckets
			}
			if s, err := a.Store.MetricTimeline(ctx, lossMetric, st.Target.Addr, since, bucket, now); err == nil && len(s) > 0 {
				pv.Loss = s[0].Buckets
			}
			v.Probes = append(v.Probes, pv)
		}
	}
	return v, nil
}

// --- Devices --------------------------------------------------------------

type deviceView struct {
	IP       string               `json:"ip"`
	IPs      []string             `json:"ips"`
	Name     string               `json:"name"`
	MAC      string               `json:"mac"`
	Source   string               `json:"source"`
	Queries  int64                `json:"queries"`
	Domains  int64                `json:"domains"`
	Problems int64                `json:"problems"`
	LastSeen int64                `json:"last_seen"`
	RxBps    *float64             `json:"rx_bps"`
	TxBps    *float64             `json:"tx_bps"`
	RxAvgBps *float64             `json:"rx_avg_bps"`
	TxAvgBps *float64             `json:"tx_avg_bps"`
	Wifi     *routeros.WifiClient `json:"wifi,omitempty"`
}

func (a *API) devices(r *http.Request) (any, error) {
	window, err := rangeParam(r)
	if err != nil {
		return nil, err
	}
	ctx := r.Context()
	now := time.Now()
	since := now.Add(-window)
	byIP := map[string]*deviceView{}
	var order []string
	get := func(ip string) *deviceView {
		d, ok := byIP[ip]
		if !ok {
			d = &deviceView{IP: ip}
			byIP[ip] = d
			order = append(order, ip)
		}
		return d
	}
	hosts, err := a.Store.Hosts(ctx)
	if err != nil {
		return nil, err
	}
	byMAC := map[string][]*deviceView{}
	for _, h := range hosts {
		d := get(h.IP)
		d.Name, d.MAC, d.Source = h.Name, h.MAC, h.Source
		if h.MAC != "" {
			byMAC[h.MAC] = append(byMAC[h.MAC], d)
		}
	}
	clients, err := a.Store.Clients(ctx, store.DNSFilter{Since: since}, maxLimit)
	if err != nil {
		return nil, err
	}
	for _, c := range clients {
		d := get(c.Client)
		d.Queries, d.Domains, d.Problems, d.LastSeen = c.Count, c.Domains, c.Problems, c.LastSeen
		if d.Name == "" {
			d.Name = c.Name
		}
		if d.MAC == "" {
			d.MAC = c.MAC
		}
	}
	for metric, set := range map[string]func(*deviceView, float64){
		routeros.MetricDeviceRx: func(d *deviceView, v float64) { d.RxBps = &v },
		routeros.MetricDeviceTx: func(d *deviceView, v float64) { d.TxBps = &v },
	} {
		latest, err := a.Store.MetricLatest(ctx, metric, now.Add(-2*time.Minute))
		if err != nil {
			return nil, err
		}
		for _, l := range latest {
			set(get(l.Key), l.Value)
		}
	}
	for metric, set := range map[string]func(*deviceView, float64){
		routeros.MetricDeviceRx: func(d *deviceView, v float64) { d.RxAvgBps = &v },
		routeros.MetricDeviceTx: func(d *deviceView, v float64) { d.TxAvgBps = &v },
	} {
		series, err := a.Store.MetricTimeline(ctx, metric, "", since, int64(window.Seconds())+1, now)
		if err != nil {
			return nil, err
		}
		for _, s := range series {
			for _, b := range s.Buckets {
				if b.Avg != nil {
					set(get(s.Key), *b.Avg)
				}
			}
		}
	}
	if a.Poller != nil {
		if snap := a.Poller.Snapshot(); snap != nil {
			for i := range snap.WifiClients {
				for _, d := range byMAC[snap.WifiClients[i].MAC] {
					d.Wifi = &snap.WifiClients[i]
				}
			}
		}
	}
	out := make([]deviceView, 0, len(order))
	for _, ip := range order {
		out = append(out, *byIP[ip])
	}
	out = groupByMAC(out)
	sortDevices(out)
	return out, nil
}

// groupByMAC folds the addresses of one physical device (IPv4 lease, IPv6
// neighbours) into a single row; addresses without a MAC stay separate.
func groupByMAC(list []deviceView) []deviceView {
	byMAC := map[string]*deviceView{}
	var out []*deviceView
	for i := range list {
		d := list[i]
		d.IPs = []string{d.IP}
		if d.MAC == "" {
			out = append(out, &d)
			continue
		}
		g, ok := byMAC[d.MAC]
		if !ok {
			byMAC[d.MAC] = &d
			out = append(out, &d)
			continue
		}
		g.IPs = append(g.IPs, d.IP)
		// Prefer an IPv4 address as the primary one.
		if strings.Contains(g.IP, ":") && !strings.Contains(d.IP, ":") {
			g.IP = d.IP
		}
		if g.Name == "" {
			g.Name = d.Name
		}
		if g.Source != "dhcp" && d.Source == "dhcp" {
			g.Source = d.Source
		}
		g.Queries += d.Queries
		g.Problems += d.Problems
		g.Domains = max(g.Domains, d.Domains)
		g.LastSeen = max(g.LastSeen, d.LastSeen)
		for dst, src := range map[**float64]*float64{&g.RxBps: d.RxBps, &g.TxBps: d.TxBps, &g.RxAvgBps: d.RxAvgBps, &g.TxAvgBps: d.TxAvgBps} {
			if *dst == nil {
				*dst = src
			}
		}
		if g.Wifi == nil {
			g.Wifi = d.Wifi
		}
	}
	res := make([]deviceView, 0, len(out))
	for _, d := range out {
		res = append(res, *d)
	}
	return res
}

func sortDevices(list []deviceView) {
	score := func(d deviceView) float64 {
		s := float64(d.Queries)
		if d.RxAvgBps != nil {
			s += *d.RxAvgBps / 1000
		}
		return s
	}
	for i := 1; i < len(list); i++ {
		for j := i; j > 0 && score(list[j]) > score(list[j-1]); j-- {
			list[j], list[j-1] = list[j-1], list[j]
		}
	}
}

// --- Events, issues, status -----------------------------------------------

func (a *API) events(w http.ResponseWriter, r *http.Request) {
	window, err := rangeParam(r)
	if err != nil {
		a.fail(w, err)
		return
	}
	q := r.URL.Query()
	f := store.EventFilter{Since: time.Now().Add(-window), Severity: q.Get("severity"), Search: strings.TrimSpace(q.Get("search"))}
	page := intParam(r, "page", 1, 1<<30)
	size := intParam(r, "page_size", 50, maxPageSize)
	items, total, err := a.Store.Events(r.Context(), f, page, size)
	if err != nil {
		a.fail(w, err)
		return
	}
	counts, err := a.Store.EventCounts(r.Context(), f.Since)
	if err != nil {
		a.fail(w, err)
		return
	}
	writeJSON(w, map[string]any{"items": items, "total": total, "page": page, "page_size": size, "counts": counts})
}

func (a *API) issues(r *http.Request) (any, error) {
	in := issues.Inputs{Thresholds: issues.DefaultThresholds()}
	if a.Poller != nil {
		in.Router = a.Poller.Snapshot()
	}
	if a.Prober != nil {
		in.Probes = a.Prober.States()
	}
	in.LastSyslog = a.Ingest.LastMessage()
	in.EverSyslog = !in.LastSyslog.IsZero()
	return issues.Evaluate(r.Context(), a.Store, in)
}

func (a *API) status(w http.ResponseWriter, _ *http.Request) {
	written, dropped := a.Store.Stats()
	out := map[string]any{
		"version":     a.Version,
		"uptime_secs": int64(time.Since(a.started).Seconds()),
		"retention": map[string]int64{
			"queries_secs": int64(a.Retention.Queries.Seconds()),
			"metrics_secs": int64(a.Retention.Metrics.Seconds()),
			"events_secs":  int64(a.Retention.Events.Seconds()),
		},
		"ingest": a.Ingest.Stats(),
		"store":  map[string]any{"written": written, "dropped": dropped, "size_bytes": a.Store.Size()},
		"router": nil,
		"probes": []probe.State{},
	}
	if a.Poller != nil {
		out["router"] = a.Poller.Snapshot()
	}
	if a.Prober != nil {
		out["probes"] = a.Prober.States()
	}
	writeJSON(w, out)
}

// --- helpers ----------------------------------------------------------------

func dnsFilter(r *http.Request) (store.DNSFilter, time.Duration, error) {
	window, err := rangeParam(r)
	if err != nil {
		return store.DNSFilter{}, 0, err
	}
	q := r.URL.Query()
	return store.DNSFilter{
		Since:   time.Now().Add(-window),
		Client:  strings.TrimSpace(q.Get("client")),
		Domain:  strings.TrimSpace(q.Get("domain")),
		Search:  strings.TrimSpace(q.Get("search")),
		Type:    strings.TrimSpace(q.Get("type")),
		Outcome: strings.TrimSpace(q.Get("outcome")),
	}, window, nil
}

func rangeParam(r *http.Request) (time.Duration, error) {
	s := r.URL.Query().Get("range")
	if s == "" {
		return defaultRange, nil
	}
	d, err := ParseDuration(s)
	if err != nil || d <= 0 || d > maxRange {
		return 0, badRequest{"invalid range"}
	}
	return d, nil
}

func intParam(r *http.Request, name string, def, max int) int {
	s := r.URL.Query().Get(name)
	if s == "" {
		return def
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < 1 {
		return def
	}
	return min(n, max)
}

// ParseDuration extends time.ParseDuration with a day suffix ("7d").
func ParseDuration(s string) (time.Duration, error) {
	if days, ok := strings.CutSuffix(s, "d"); ok {
		n, err := strconv.Atoi(days)
		if err != nil {
			return 0, err
		}
		return time.Duration(n) * 24 * time.Hour, nil
	}
	return time.ParseDuration(s)
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

func writeRaw(w http.ResponseWriter, body []byte) {
	w.Header().Set("Content-Type", "application/json")
	w.Write(body)
}

func httpError(w http.ResponseWriter, msg string, code int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": msg})
}
