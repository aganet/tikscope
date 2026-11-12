package store

import (
	"bytes"
	"context"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func testStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "test.db"), slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestMigrateIsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "m.db")
	for i := 0; i < 2; i++ {
		s, err := Open(path, slog.Default())
		if err != nil {
			t.Fatalf("open %d: %v", i, err)
		}
		var v int
		if err := s.rw.QueryRow(`PRAGMA user_version`).Scan(&v); err != nil || v != schemaVersion {
			t.Fatalf("user_version = %d, err %v", v, err)
		}
		s.Close()
	}
}

func TestDNSReads(t *testing.T) {
	s := testStore(t)
	now := time.Now().Truncate(time.Second)
	s.insertQueries([]Query{
		{TS: now.Add(-10 * time.Minute), Router: "r", Client: "10.0.0.1", Domain: "example.com", Type: "A", Outcome: "ok", Answer: "1.1.1.1", LatencyMS: 10},
		{TS: now.Add(-9 * time.Minute), Router: "r", Client: "10.0.0.1", Domain: "example.com", Type: "AAAA", Outcome: "nodata", LatencyMS: 30},
		{TS: now.Add(-8 * time.Minute), Router: "r", Client: "10.0.0.2", Domain: "ads.example", Type: "A", Outcome: "blocked", Answer: "0.0.0.0", LatencyMS: 1},
		{TS: now.Add(-7 * time.Minute), Router: "r", Client: "fd00::2", Domain: "nope.example", Type: "A", Outcome: "nxdomain", LatencyMS: 200},
		{TS: now.Add(-6 * time.Minute), Router: "r", Client: "10.0.0.2", Domain: "under_score.example", Type: "A", LatencyMS: -1},
		{TS: now.Add(-3 * time.Hour), Router: "r", Client: "10.0.0.3", Domain: "old.example", Type: "A", Outcome: "ok", LatencyMS: 5},
	})
	s.upsertHosts([]Host{
		{IP: "10.0.0.1", MAC: "AA:AA:AA:AA:AA:AA", Name: "laptop", Source: "dhcp"},
		{IP: "fd00::2", MAC: "BB:BB:BB:BB:BB:BB", Source: "nd"},
		{IP: "10.0.0.2", MAC: "BB:BB:BB:BB:BB:BB", Name: "phone", Source: "dhcp"},
		{IP: "10.0.0.1", MAC: "AA:AA:AA:AA:AA:AA", Source: "arp"},
	})
	ctx := context.Background()

	sum, err := s.DNSSummary(ctx, time.Hour, 10)
	if err != nil {
		t.Fatal(err)
	}
	tot := sum.Totals
	if tot.Queries != 5 || tot.Clients != 3 || tot.Domains != 4 || tot.IPv6 != 1 {
		t.Errorf("totals: %+v", tot)
	}
	if tot.Outcomes["ok"] != 1 || tot.Outcomes["blocked"] != 1 || tot.Outcomes[""] != 1 {
		t.Errorf("outcomes: %+v", tot.Outcomes)
	}
	if tot.LatencyMS == nil || *tot.LatencyMS != 60.25 {
		t.Errorf("latency: %v", tot.LatencyMS)
	}
	if len(sum.Types) != 2 || sum.Types[0].Key != "A" || sum.Types[0].Count != 4 {
		t.Errorf("types: %+v", sum.Types)
	}
	if len(sum.TopDomains) != 4 || sum.TopDomains[0].Domain != "example.com" || sum.TopDomains[0].Count != 2 || sum.TopDomains[0].Clients != 1 {
		t.Errorf("top domains: %+v", sum.TopDomains)
	}
	if len(sum.Problems) != 2 {
		t.Errorf("problems: %+v", sum.Problems)
	}
	if len(sum.TopClients) != 3 || sum.TopClients[0].Client != "10.0.0.1" || sum.TopClients[0].Name != "laptop" {
		t.Errorf("top clients: %+v", sum.TopClients)
	}
	for _, c := range sum.TopClients {
		if c.Client == "fd00::2" && c.Name != "phone" {
			t.Errorf("ipv6 client should inherit the lease name via MAC: %+v", c)
		}
	}
	if sum.BucketSecs != 60 || len(sum.Timeline) != 61 {
		t.Errorf("timeline: bucket %d, %d points", sum.BucketSecs, len(sum.Timeline))
	}

	rows, n, err := s.Queries(ctx, DNSFilter{Since: now.Add(-time.Hour), Search: "under_"}, 1, 10)
	if err != nil || n != 1 || len(rows) != 1 || rows[0].Domain != "under_score.example" {
		t.Errorf("search with wildcard char: n=%d rows=%+v err=%v", n, rows, err)
	}
	rows, n, err = s.Queries(ctx, DNSFilter{Since: now.Add(-time.Hour), Outcome: "problem"}, 1, 10)
	if err != nil || n != 2 || len(rows) != 2 {
		t.Errorf("problem filter: n=%d rows=%+v err=%v", n, rows, err)
	}
	rows, n, err = s.Queries(ctx, DNSFilter{Since: now.Add(-time.Hour), Client: "10.0.0.1"}, 2, 1)
	if err != nil || n != 2 || len(rows) != 1 || rows[0].Type != "A" || rows[0].Name != "laptop" {
		t.Errorf("paged client filter: n=%d rows=%+v err=%v", n, rows, err)
	}

	var buf bytes.Buffer
	if err := s.ExportCSV(ctx, DNSFilter{Since: now.Add(-time.Hour), Domain: "example.com"}, &buf); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 3 || !strings.Contains(lines[1], ",laptop,example.com,AAAA,nodata,,30") {
		t.Errorf("csv:\n%s", buf.String())
	}

	s.purge(Retention{Queries: 2 * time.Hour, Metrics: time.Hour, Events: time.Hour})
	if _, n, err := s.Queries(ctx, DNSFilter{Since: now.Add(-24 * time.Hour)}, 1, 10); err != nil || n != 5 {
		t.Errorf("after purge: n=%d err=%v", n, err)
	}
}

func TestMetricsAndEvents(t *testing.T) {
	s := testStore(t)
	now := time.Now().Truncate(time.Minute)
	var samples []Sample
	for i := 0; i < 20; i++ {
		ts := now.Add(-time.Duration(i) * 15 * time.Second)
		samples = append(samples, Sample{TS: ts, Metric: "iface.rx_bps", Key: "ether1", Value: float64(1000 * (i + 1))})
		samples = append(samples, Sample{TS: ts, Metric: "icmp.loss", Key: "1.1.1.1", Value: float64(i % 2)})
	}
	s.insertSamples(samples)
	ctx := context.Background()

	series, err := s.MetricTimeline(ctx, "iface.rx_bps", "", now.Add(-10*time.Minute), 60, now)
	if err != nil || len(series) != 1 || series[0].Key != "ether1" || len(series[0].Buckets) != 11 {
		t.Fatalf("timeline: %+v err=%v", series, err)
	}
	filled := 0
	for _, b := range series[0].Buckets {
		if b.N > 0 {
			filled++
			if b.Avg == nil || b.Max == nil || *b.Max < *b.Avg {
				t.Errorf("bucket %+v", b)
			}
		}
	}
	if filled != 6 {
		t.Errorf("expected 6 populated buckets, got %d", filled)
	}
	latest, err := s.MetricLatest(ctx, "iface.rx_bps", now.Add(-time.Hour))
	if err != nil || len(latest) != 1 || latest[0].Value != 1000 {
		t.Errorf("latest: %+v err=%v", latest, err)
	}
	agg, err := s.MetricAggregate(ctx, "icmp.loss", "1.1.1.1", now.Add(-time.Hour))
	if err != nil || agg.N != 20 || agg.Avg == nil || *agg.Avg != 0.5 {
		t.Errorf("aggregate: %+v err=%v", agg, err)
	}
	keys, err := s.MetricKeys(ctx, "icmp.loss", now.Add(-time.Hour))
	if err != nil || len(keys) != 1 || keys[0] != "1.1.1.1" {
		t.Errorf("keys: %v err=%v", keys, err)
	}

	s.insertEvents([]Event{
		{TS: now.Add(-time.Minute), Router: "r", Topics: "interface,error", Severity: "error", Message: "ether1 link down"},
		{TS: now.Add(-2 * time.Minute), Router: "r", Topics: "dhcp,info", Severity: "info", Message: "lease assigned"},
		{TS: now.Add(-3 * time.Minute), Router: "r", Topics: "system,warning", Severity: "warning", Message: "100% CPU"},
	})
	items, total, err := s.Events(ctx, EventFilter{Since: now.Add(-time.Hour), Severity: "problem"}, 1, 10)
	if err != nil || total != 2 || len(items) != 2 || items[0].Severity != "error" {
		t.Errorf("events: total=%d items=%+v err=%v", total, items, err)
	}
	items, total, err = s.Events(ctx, EventFilter{Since: now.Add(-time.Hour), Search: "100%"}, 1, 10)
	if err != nil || total != 1 || items[0].Message != "100% CPU" {
		t.Errorf("event search with wildcard: total=%d items=%+v err=%v", total, items, err)
	}
	counts, err := s.EventCounts(ctx, now.Add(-time.Hour))
	if err != nil || counts["info"] != 1 || counts["error"] != 1 || counts["warning"] != 1 {
		t.Errorf("counts: %v err=%v", counts, err)
	}
}
