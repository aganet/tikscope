// Package issues derives a short list of current problems from recent
// metrics, DNS outcomes and events.
package issues

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/aganet/tikscope/internal/probe"
	"github.com/aganet/tikscope/internal/routeros"
	"github.com/aganet/tikscope/internal/store"
)

type Issue struct {
	Severity string `json:"severity"` // warning, error, critical
	Title    string `json:"title"`
	Detail   string `json:"detail"`
	Kind     string `json:"kind"` // probe, dns, interface, router, collector, event
	Key      string `json:"key,omitempty"`
}

// Inputs is everything the evaluation looks at besides the store.
type Inputs struct {
	Router     *routeros.Snapshot // nil when polling is not configured
	Probes     []probe.State
	LastSyslog time.Time
	EverSyslog bool
	Thresholds Thresholds
}

type Thresholds struct {
	LossPct     float64 // packet loss over the short window
	LatencyMS   float64 // absolute RTT floor before a spike counts
	LatencyMult float64 // short window RTT vs long window
	DNSFailPct  float64 // servfail+error share of answered queries
	CPUPct      float64
	MemPct      float64
	ConnPct     float64
	SyslogGap   time.Duration
}

func DefaultThresholds() Thresholds {
	return Thresholds{LossPct: 5, LatencyMS: 40, LatencyMult: 3, DNSFailPct: 3, CPUPct: 85, MemPct: 90, ConnPct: 85, SyslogGap: 5 * time.Minute}
}

const (
	short = 5 * time.Minute
	long  = time.Hour
)

func Evaluate(ctx context.Context, st *store.Store, in Inputs) ([]Issue, error) {
	now := time.Now()
	th := in.Thresholds
	out := []Issue{}

	// Reachability and latency from the probes.
	for _, p := range in.Probes {
		k := p.Target.Kind + " " + p.Target.Addr
		if !p.Up {
			out = append(out, Issue{Severity: "critical", Kind: "probe", Key: p.Target.Addr,
				Title: fmt.Sprintf("%s unreachable", k), Detail: fmt.Sprintf("down since %s: %s", ago(p.Since, now), p.LastError)})
			continue
		}
		lossMetric, rttMetric := probe.MetricICMPLoss, probe.MetricICMPRTT
		if p.Target.Kind == "dns" {
			lossMetric, rttMetric = probe.MetricDNSLoss, probe.MetricDNSRTT
		}
		loss, err := st.MetricAggregate(ctx, lossMetric, p.Target.Addr, now.Add(-short), now)
		if err != nil {
			return nil, err
		}
		if loss.Avg != nil && loss.N >= 3 && *loss.Avg*100 >= th.LossPct {
			sev := "warning"
			if *loss.Avg*100 >= 25 {
				sev = "error"
			}
			out = append(out, Issue{Severity: sev, Kind: "probe", Key: p.Target.Addr,
				Title: fmt.Sprintf("Packet loss to %s", p.Target.Addr), Detail: fmt.Sprintf("%.0f%% of %s probes failed in the last 5 minutes", *loss.Avg*100, p.Target.Kind)})
		}
		recent, err := st.MetricAggregate(ctx, rttMetric, p.Target.Addr, now.Add(-short), now)
		if err != nil {
			return nil, err
		}
		base, err := st.MetricAggregate(ctx, rttMetric, p.Target.Addr, now.Add(-long), now)
		if err != nil {
			return nil, err
		}
		if recent.Avg != nil && base.Avg != nil && recent.N >= 3 && *recent.Avg >= th.LatencyMS && *recent.Avg >= th.LatencyMult**base.Avg {
			out = append(out, Issue{Severity: "warning", Kind: "probe", Key: p.Target.Addr,
				Title: fmt.Sprintf("Latency spike to %s", p.Target.Addr), Detail: fmt.Sprintf("%.0f ms average now, %.0f ms over the last hour", *recent.Avg, *base.Avg)})
		}
	}

	// DNS health from the router's own answers.
	sum, _, err := dnsShort(ctx, st, now.Add(-short))
	if err != nil {
		return nil, err
	}
	answered := sum.Queries - sum.Outcomes[""]
	if answered >= 10 {
		failed := sum.Outcomes["servfail"] + sum.Outcomes["error"]
		if pct := 100 * float64(failed) / float64(answered); pct >= th.DNSFailPct {
			out = append(out, Issue{Severity: "error", Kind: "dns", Title: "DNS resolution failures",
				Detail: fmt.Sprintf("%.1f%% of answers in the last 5 minutes were SERVFAIL or errors (%d of %d)", pct, failed, answered)})
		}
	}
	if sum.Queries >= 20 && answered < sum.Queries/2 {
		longSum, _, err := dnsShort(ctx, st, now.Add(-long))
		if err != nil {
			return nil, err
		}
		longAnswered := longSum.Queries - longSum.Outcomes[""]
		// Only meaningful on routers that normally log answers.
		if longSum.Queries > 0 && float64(longAnswered)/float64(longSum.Queries) > 0.8 {
			out = append(out, Issue{Severity: "warning", Kind: "dns", Title: "Router stopped answering DNS queries",
				Detail: fmt.Sprintf("only %d of %d queries in the last 5 minutes got an answer line", answered, sum.Queries)})
		}
	}
	if sum.LatencyMS != nil && *sum.LatencyMS >= 500 {
		out = append(out, Issue{Severity: "warning", Kind: "dns", Title: "Slow DNS answers",
			Detail: fmt.Sprintf("average response time %.0f ms over the last 5 minutes", *sum.LatencyMS)})
	}

	// Interface errors and link flaps. Drops are never alerted and errors
	// are ignored on Wi-Fi, where retransmissions are counted as errors.
	wireless := map[string]bool{}
	if in.Router != nil {
		for _, i := range in.Router.Interfaces {
			if i.Type == "wifi" || i.Type == "wlan" || i.Type == "wireless" || strings.HasPrefix(i.Type, "cap") {
				wireless[i.Name] = true
			}
		}
	}
	for _, metric := range []string{routeros.MetricIfaceLinkDown, routeros.MetricIfaceErrors} {
		keys, err := st.MetricKeys(ctx, metric, now.Add(-short))
		if err != nil {
			return nil, err
		}
		for _, k := range keys {
			if metric == routeros.MetricIfaceErrors && wireless[k] {
				continue
			}
			agg, err := st.MetricAggregate(ctx, metric, k, now.Add(-short), now)
			if err != nil || agg.Sum == nil {
				continue
			}
			if metric == routeros.MetricIfaceLinkDown {
				out = append(out, Issue{Severity: "error", Kind: "interface", Key: k, Title: fmt.Sprintf("Link flapping on %s", k),
					Detail: fmt.Sprintf("%.0f link down events in the last 5 minutes", *agg.Sum)})
			} else if *agg.Sum >= 10 {
				out = append(out, Issue{Severity: "warning", Kind: "interface", Key: k, Title: fmt.Sprintf("Errors on %s", k),
					Detail: fmt.Sprintf("%.0f errors in the last 5 minutes", *agg.Sum)})
			}
		}
	}

	// Router resources.
	if r := in.Router; r != nil {
		if r.Error != "" {
			out = append(out, Issue{Severity: "warning", Kind: "router", Title: "Router API poll failing", Detail: firstLine(r.Error)})
		}
		if r.Identity != "" {
			if cpu, err := st.MetricAggregate(ctx, routeros.MetricCPU, "router", now.Add(-short), now); err == nil && cpu.Avg != nil && *cpu.Avg >= th.CPUPct {
				out = append(out, Issue{Severity: "warning", Kind: "router", Title: "High CPU load", Detail: fmt.Sprintf("%.0f%% average over the last 5 minutes", *cpu.Avg)})
			}
			if r.TotalMemory > 0 {
				if used := 100 * float64(r.TotalMemory-r.FreeMemory) / float64(r.TotalMemory); used >= th.MemPct {
					out = append(out, Issue{Severity: "warning", Kind: "router", Title: "Memory almost full", Detail: fmt.Sprintf("%.0f%% of RAM in use", used)})
				}
			}
			if r.MaxConns > 0 {
				if used := 100 * float64(r.Connections) / float64(r.MaxConns); used >= th.ConnPct {
					out = append(out, Issue{Severity: "error", Kind: "router", Title: "Connection table almost full", Detail: fmt.Sprintf("%d of %d tracked connections", r.Connections, r.MaxConns)})
				}
			}
			for _, w := range r.Netwatch {
				if w.Status == "down" {
					label := w.Host
					if w.Comment != "" {
						label = w.Comment + " (" + w.Host + ")"
					}
					out = append(out, Issue{Severity: "error", Kind: "router", Key: w.Host, Title: "Netwatch: " + label + " is down", Detail: "since " + w.Since})
				}
			}
			for _, peer := range r.WireGuard {
				// Peers with an endpoint are expected to keep a session alive.
				if !peer.Disabled && peer.Endpoint != "" && (peer.HandshakeAge < 0 || peer.HandshakeAge > 15*60) {
					age := "never"
					if peer.HandshakeAge >= 0 {
						age = peer.LastHandshake + " ago"
					}
					out = append(out, Issue{Severity: "warning", Kind: "router", Key: peer.Name, Title: fmt.Sprintf("WireGuard peer %s on %s is silent", peer.Name, peer.Interface), Detail: "last handshake " + age})
				}
			}
			for _, pool := range r.Pools {
				if pool.UsedPct >= 90 {
					out = append(out, Issue{Severity: "warning", Kind: "router", Key: pool.Name, Title: fmt.Sprintf("DHCP pool %s almost full", pool.Name), Detail: fmt.Sprintf("%d of %d addresses leased", pool.Used, pool.Size)})
				}
			}
			if r.WAN != "" {
				for _, i := range r.Interfaces {
					if i.WAN && !i.Running {
						out = append(out, Issue{Severity: "critical", Kind: "interface", Key: i.Name, Title: fmt.Sprintf("WAN interface %s is down", i.Name), Detail: "the interface behind the default route is not running"})
					}
				}
			}
		}
	}

	// The log stream itself.
	if in.EverSyslog && now.Sub(in.LastSyslog) > th.SyslogGap {
		out = append(out, Issue{Severity: "warning", Kind: "collector", Title: "No syslog received",
			Detail: fmt.Sprintf("last line arrived %s", ago(in.LastSyslog.Unix(), now))})
	}

	// Error and critical log lines from the router, grouped by message.
	events, _, err := st.Events(ctx, store.EventFilter{Since: now.Add(-15 * time.Minute), Severity: "problem"}, 1, 200)
	if err != nil {
		return nil, err
	}
	seen := map[string]int{}
	for _, e := range events {
		if e.Severity == "warning" || e.Router == "tikscope" {
			continue
		}
		seen[e.Message]++
		if seen[e.Message] > 1 {
			continue
		}
		out = append(out, Issue{Severity: e.Severity, Kind: "event", Key: e.Topics, Title: "Router logged: " + truncate(e.Message, 80),
			Detail: fmt.Sprintf("%s, %s", e.Topics, ago(e.TS, now))})
	}
	for i := range out {
		if n := seen[strings.TrimPrefix(out[i].Title, "Router logged: ")]; n > 1 {
			out[i].Detail += fmt.Sprintf(", %d times", n)
		}
	}
	sortBySeverity(out)
	return out, nil
}

func dnsShort(ctx context.Context, st *store.Store, since time.Time) (store.DNSTotals, []store.Count, error) {
	return st.DNSTotalsSince(ctx, since)
}

var severityRank = map[string]int{"critical": 0, "error": 1, "warning": 2}

func sortBySeverity(list []Issue) {
	for i := 1; i < len(list); i++ {
		for j := i; j > 0 && severityRank[list[j].Severity] < severityRank[list[j-1].Severity]; j-- {
			list[j], list[j-1] = list[j-1], list[j]
		}
	}
}

func ago(ts int64, now time.Time) string {
	d := now.Sub(time.Unix(ts, 0)).Round(time.Second)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds ago", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	default:
		return fmt.Sprintf("%dh %dm ago", int(d.Hours()), int(d.Minutes())%60)
	}
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-3] + "..."
}
