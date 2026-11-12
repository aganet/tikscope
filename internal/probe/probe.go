// Package probe measures reachability and latency from the collector:
// ICMP echo to arbitrary hosts and real DNS lookups against resolvers.
package probe

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"sync"
	"time"

	"golang.org/x/net/icmp"
	"golang.org/x/net/ipv4"
	"golang.org/x/net/ipv6"

	"github.com/aganet/tikscope/internal/store"
)

const (
	MetricICMPRTT  = "icmp.rtt_ms"
	MetricICMPLoss = "icmp.loss"
	MetricDNSRTT   = "dnsprobe.rtt_ms"
	MetricDNSLoss  = "dnsprobe.loss"

	probeTimeout = 2 * time.Second
	downAfter    = 3 // consecutive failures before a target is reported down
)

type Target struct {
	Kind string `json:"kind"` // icmp or dns
	Addr string `json:"addr"` // host for icmp, host[:port] for dns
	Name string `json:"name"` // name to resolve for dns probes
}

type State struct {
	Target    Target  `json:"target"`
	Up        bool    `json:"up"`
	Since     int64   `json:"since"`
	LastRTTMS float64 `json:"last_rtt_ms"`
	LastError string  `json:"last_error,omitempty"`
	Failures  int     `json:"consecutive_failures"`
}

type Prober struct {
	targets  []Target
	interval time.Duration
	store    *store.Store
	log      *slog.Logger

	mu     sync.Mutex
	states map[string]*State
}

func New(targets []Target, interval time.Duration, st *store.Store, log *slog.Logger) *Prober {
	p := &Prober{targets: targets, interval: interval, store: st, log: log, states: map[string]*State{}}
	for _, t := range targets {
		p.states[key(t)] = &State{Target: t, Up: true, Since: time.Now().Unix()}
	}
	return p
}

func key(t Target) string { return t.Kind + ":" + t.Addr }

func (p *Prober) States() []State {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]State, 0, len(p.targets))
	for _, t := range p.targets {
		out = append(out, *p.states[key(t)])
	}
	return out
}

func (p *Prober) Run(ctx context.Context) {
	if len(p.targets) == 0 {
		return
	}
	t := time.NewTicker(p.interval)
	defer t.Stop()
	for {
		p.round(ctx)
		select {
		case <-t.C:
		case <-ctx.Done():
			return
		}
	}
}

func (p *Prober) round(ctx context.Context) {
	var wg sync.WaitGroup
	for _, t := range p.targets {
		wg.Add(1)
		go func() {
			defer wg.Done()
			now := time.Now()
			var rtt time.Duration
			var err error
			switch t.Kind {
			case "dns":
				rtt, err = dnsLookup(ctx, t.Addr, t.Name)
			default:
				rtt, err = ping(ctx, t.Addr)
			}
			p.record(t, now, rtt, err)
		}()
	}
	wg.Wait()
}

func (p *Prober) record(t Target, now time.Time, rtt time.Duration, err error) {
	rttMetric, lossMetric := MetricICMPRTT, MetricICMPLoss
	if t.Kind == "dns" {
		rttMetric, lossMetric = MetricDNSRTT, MetricDNSLoss
	}
	loss := 0.0
	if err != nil {
		loss = 1
	} else {
		p.store.AddSample(store.Sample{TS: now, Metric: rttMetric, Key: t.Addr, Value: float64(rtt.Microseconds()) / 1000})
	}
	p.store.AddSample(store.Sample{TS: now, Metric: lossMetric, Key: t.Addr, Value: loss})

	p.mu.Lock()
	defer p.mu.Unlock()
	st := p.states[key(t)]
	if err != nil {
		st.Failures++
		st.LastError = err.Error()
		if st.Up && st.Failures >= downAfter {
			st.Up = false
			st.Since = now.Unix()
			p.store.AddEvent(store.Event{TS: now, Router: "tikscope", Topics: "probe,error", Severity: "error",
				Message: fmt.Sprintf("%s probe to %s is down: %v", t.Kind, t.Addr, err)})
		}
		return
	}
	st.LastRTTMS = float64(rtt.Microseconds()) / 1000
	st.LastError = ""
	if !st.Up {
		down := now.Unix() - st.Since
		st.Up = true
		st.Since = now.Unix()
		p.store.AddEvent(store.Event{TS: now, Router: "tikscope", Topics: "probe,info", Severity: "info",
			Message: fmt.Sprintf("%s probe to %s recovered after %ds", t.Kind, t.Addr, down)})
	}
	st.Failures = 0
}

// dnsLookup resolves name through the given server and reports the time it took.
func dnsLookup(ctx context.Context, server, name string) (time.Duration, error) {
	if _, _, err := net.SplitHostPort(server); err != nil {
		server = net.JoinHostPort(server, "53")
	}
	r := &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return (&net.Dialer{Timeout: probeTimeout}).DialContext(ctx, network, server)
		},
	}
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	start := time.Now()
	_, err := r.LookupHost(ctx, name)
	var dnsErr *net.DNSError
	// NXDOMAIN still proves the resolver answered.
	if errors.As(err, &dnsErr) && dnsErr.IsNotFound {
		err = nil
	}
	return time.Since(start), err
}

// ping sends one echo request. A raw socket is used when permitted (root or
// CAP_NET_RAW); otherwise the unprivileged datagram ICMP socket, which needs
// net.ipv4.ping_group_range to include the process group.
func ping(ctx context.Context, host string) (time.Duration, error) {
	ip, err := resolveIP(ctx, host)
	if err != nil {
		return 0, err
	}
	var echoType icmp.Type = ipv4.ICMPTypeEcho
	proto, rawNet, dgramNet, listen := 1, "ip4:icmp", "udp4", "0.0.0.0"
	if ip.To4() == nil {
		echoType = ipv6.ICMPTypeEchoRequest
		proto, rawNet, dgramNet, listen = 58, "ip6:ipv6-icmp", "udp6", "::"
	}
	conn, err := icmp.ListenPacket(rawNet, listen)
	raw := err == nil
	if err != nil {
		if conn, err = icmp.ListenPacket(dgramNet, listen); err != nil {
			return 0, fmt.Errorf("icmp socket: %w (needs CAP_NET_RAW or net.ipv4.ping_group_range)", err)
		}
	}
	defer conn.Close()

	id := os.Getpid() & 0xffff
	seq := int(time.Now().UnixNano() & 0xffff)
	payload := []byte(fmt.Sprintf("tikscope %d", seq))
	msg, err := (&icmp.Message{Type: echoType, Code: 0, Body: &icmp.Echo{ID: id, Seq: seq, Data: payload}}).Marshal(nil)
	if err != nil {
		return 0, err
	}
	var dst net.Addr = &net.IPAddr{IP: ip}
	if !raw {
		dst = &net.UDPAddr{IP: ip}
	}
	start := time.Now()
	if _, err := conn.WriteTo(msg, dst); err != nil {
		return 0, err
	}
	deadline := start.Add(probeTimeout)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	conn.SetReadDeadline(deadline)
	buf := make([]byte, 1500)
	for {
		n, peer, err := conn.ReadFrom(buf)
		if err != nil {
			return 0, fmt.Errorf("timeout")
		}
		if !addrIs(peer, ip) {
			continue
		}
		reply, err := icmp.ParseMessage(proto, buf[:n])
		if err != nil {
			continue
		}
		echo, ok := reply.Body.(*icmp.Echo)
		if !ok || (reply.Type != ipv4.ICMPTypeEchoReply && reply.Type != ipv6.ICMPTypeEchoReply) {
			continue
		}
		// Datagram sockets rewrite the id, so the sequence identifies our packet.
		if echo.Seq != seq {
			continue
		}
		return time.Since(start), nil
	}
}

func resolveIP(ctx context.Context, host string) (net.IP, error) {
	if ip := net.ParseIP(host); ip != nil {
		return ip, nil
	}
	ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, err
	}
	if len(ips) == 0 {
		return nil, fmt.Errorf("no address for %s", host)
	}
	return ips[0].IP, nil
}

func addrIs(a net.Addr, ip net.IP) bool {
	switch v := a.(type) {
	case *net.IPAddr:
		return v.IP.Equal(ip)
	case *net.UDPAddr:
		return v.IP.Equal(ip)
	}
	return false
}
