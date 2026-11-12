// Package ingest receives RouterOS syslog over UDP and turns it into stored
// queries, events and device identities.
package ingest

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/netip"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/aganet/tikscope/internal/store"
	"github.com/aganet/tikscope/internal/syslog"
)

const (
	// How long a query waits for its answer line before being stored without
	// one. RouterOS 6 never logs answers, so this is also the visibility
	// delay on those routers.
	answerTimeout = 5 * time.Second
	flushInterval = 500 * time.Millisecond
	udpReadBuffer = 4 << 20
)

// Sink is the part of the store the ingester writes to.
type Sink interface {
	AddQuery(store.Query)
	AddEvent(store.Event)
	AddHost(store.Host)
}

type pending struct {
	q        store.Query
	received time.Time
	done     bool
	answers  []string
}

type Ingester struct {
	sink    Sink
	log     *slog.Logger
	allowed []netip.Prefix // empty means any source

	mu      sync.Mutex
	pending map[string]*pending
	routers map[string]time.Time

	received atomic.Int64
	queries  atomic.Int64
	answered atomic.Int64
	orphans  atomic.Int64
	events   atomic.Int64
	leases   atomic.Int64
	rejected atomic.Int64
}

type Stats struct {
	Received int64            `json:"received"`
	Queries  int64            `json:"queries"`
	Answered int64            `json:"answered"`
	Orphans  int64            `json:"orphan_answers"`
	Events   int64            `json:"events"`
	Leases   int64            `json:"leases"`
	Rejected int64            `json:"rejected"` // datagrams from sources outside the allowlist
	Pending  int              `json:"pending"`
	Routers  map[string]int64 `json:"routers"` // last message time per source address
}

func New(sink Sink, allowed []netip.Prefix, log *slog.Logger) *Ingester {
	return &Ingester{sink: sink, log: log, allowed: allowed, pending: map[string]*pending{}, routers: map[string]time.Time{}}
}

// ParseSources reads a comma separated list of addresses or CIDR prefixes.
func ParseSources(s string) ([]netip.Prefix, error) {
	var out []netip.Prefix
	for part := range strings.SplitSeq(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if p, err := netip.ParsePrefix(part); err == nil {
			out = append(out, p)
			continue
		}
		a, err := netip.ParseAddr(part)
		if err != nil {
			return nil, err
		}
		out = append(out, netip.PrefixFrom(a, a.BitLen()))
	}
	return out, nil
}

func (in *Ingester) allowedSource(ip net.IP) bool {
	if len(in.allowed) == 0 {
		return true
	}
	a, ok := netip.AddrFromSlice(ip)
	if !ok {
		return false
	}
	a = a.Unmap()
	for _, p := range in.allowed {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

// Listen receives datagrams until ctx is cancelled.
func (in *Ingester) Listen(ctx context.Context, addr string) error {
	udpAddr, err := net.ResolveUDPAddr("udp", addr)
	if err != nil {
		return err
	}
	conn, err := net.ListenUDP("udp", udpAddr)
	if err != nil {
		return err
	}
	if err := conn.SetReadBuffer(udpReadBuffer); err != nil {
		in.log.Warn("set udp read buffer", "err", err)
	}
	in.log.Info("listening for syslog", "addr", conn.LocalAddr())

	go in.flushLoop(ctx)
	go func() {
		<-ctx.Done()
		conn.Close()
	}()

	buf := make([]byte, 65535)
	for {
		n, from, err := conn.ReadFromUDP(buf)
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				return nil
			}
			in.log.Warn("udp read", "err", err)
			continue
		}
		if !in.allowedSource(from.IP) {
			in.rejected.Add(1)
			continue
		}
		now := time.Now()
		router := from.IP.String()
		for line := range strings.SplitSeq(string(buf[:n]), "\n") {
			if strings.TrimSpace(line) != "" {
				in.Handle(router, line, now)
			}
		}
	}
}

// Handle processes one log line from the given router.
func (in *Ingester) Handle(router, raw string, now time.Time) {
	in.received.Add(1)
	in.mu.Lock()
	in.routers[router] = now
	in.mu.Unlock()

	line := syslog.Parse(raw)
	switch line.Kind {
	case syslog.KindQuery:
		in.queries.Add(1)
		client := line.Client
		if client == "" {
			client = router
		}
		p := &pending{
			q:        store.Query{TS: now, Router: router, Client: client, Domain: line.Domain, Type: line.Type, LatencyMS: -1},
			received: now,
		}
		key := router + "#" + line.ID
		in.mu.Lock()
		if old := in.pending[key]; old != nil {
			in.emit(old)
		}
		in.pending[key] = p
		in.mu.Unlock()
	case syslog.KindAnswer:
		key := router + "#" + line.ID
		in.mu.Lock()
		p := in.pending[key]
		if p == nil {
			in.mu.Unlock()
			in.orphans.Add(1)
			return
		}
		if !p.done {
			p.done = true
			p.q.LatencyMS = now.Sub(p.received).Milliseconds()
			in.answered.Add(1)
		}
		// Later lines for the same id (one per record) only add answers, but
		// a blocked or error outcome always wins over a plain ok.
		if p.q.Outcome == "" || p.q.Outcome == syslog.OutcomeOK {
			p.q.Outcome = line.Outcome
		}
		if line.Domain != "" && line.Domain != p.q.Domain {
			p.answers = append(p.answers, line.Domain)
		}
		p.answers = append(p.answers, line.Answers...)
		in.mu.Unlock()
	default:
		in.events.Add(1)
		in.sink.AddEvent(store.Event{TS: now, Router: router, Topics: strings.Join(line.Topics, ","), Severity: line.Severity, Message: line.Message})
		if line.Lease != nil {
			in.leases.Add(1)
			in.sink.AddHost(store.Host{IP: line.Lease.IP, MAC: line.Lease.MAC, Name: line.Lease.Name, Source: "dhcp"})
		}
	}
}

// emit must be called with in.mu held.
func (in *Ingester) emit(p *pending) {
	p.q.Answer = strings.Join(p.answers, " ")
	in.sink.AddQuery(p.q)
}

func (in *Ingester) flushLoop(ctx context.Context) {
	t := time.NewTicker(flushInterval)
	defer t.Stop()
	for {
		select {
		case <-t.C:
			in.flush(time.Now(), false)
		case <-ctx.Done():
			return
		}
	}
}

func (in *Ingester) flush(now time.Time, all bool) {
	in.mu.Lock()
	defer in.mu.Unlock()
	for key, p := range in.pending {
		if all || p.done || now.Sub(p.received) > answerTimeout {
			in.emit(p)
			delete(in.pending, key)
		}
	}
}

// Close hands every pending query to the sink, answered or not.
func (in *Ingester) Close() { in.flush(time.Now(), true) }

func (in *Ingester) Stats() Stats {
	in.mu.Lock()
	routers := make(map[string]int64, len(in.routers))
	for r, t := range in.routers {
		routers[r] = t.Unix()
	}
	pending := len(in.pending)
	in.mu.Unlock()
	return Stats{
		Received: in.received.Load(),
		Queries:  in.queries.Load(),
		Answered: in.answered.Load(),
		Orphans:  in.orphans.Load(),
		Events:   in.events.Load(),
		Leases:   in.leases.Load(),
		Rejected: in.rejected.Load(),
		Pending:  pending,
		Routers:  routers,
	}
}

// LastMessage reports the most recent syslog line from any router.
func (in *Ingester) LastMessage() time.Time {
	in.mu.Lock()
	defer in.mu.Unlock()
	var last time.Time
	for _, t := range in.routers {
		if t.After(last) {
			last = t
		}
	}
	return last
}
