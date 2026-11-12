package ingest

import (
	"log/slog"
	"testing"
	"time"

	"github.com/aganet/tikscope/internal/store"
)

type memSink struct {
	queries []store.Query
	events  []store.Event
	hosts   []store.Host
}

func (m *memSink) AddQuery(q store.Query) { m.queries = append(m.queries, q) }
func (m *memSink) AddEvent(e store.Event) { m.events = append(m.events, e) }
func (m *memSink) AddHost(h store.Host)   { m.hosts = append(m.hosts, h) }

func TestCorrelation(t *testing.T) {
	sink := &memSink{}
	in := New(sink, nil, slog.Default())
	t0 := time.Unix(1_700_000_000, 0)

	in.Handle("10.0.0.1", "dns query from 192.168.88.10: #1 example.com. A", t0)
	in.Handle("10.0.0.1", "dns query from 192.168.88.11: #2 ads.example.com. A", t0)
	in.Handle("10.0.0.1", "dns local query: #3 cloud.mikrotik.com. A", t0)
	in.Handle("10.0.0.1", "dns query from 192.168.88.12: #4 missing.example. AAAA", t0)
	in.Handle("10.0.0.1", "dns done query: #1 example.com 93.184.216.34", t0.Add(42*time.Millisecond))
	in.Handle("10.0.0.1", "dns done query: #1 example.com 93.184.216.35", t0.Add(43*time.Millisecond))
	in.Handle("10.0.0.1", "dns done query: #2 ads.example.com 0.0.0.0", t0.Add(time.Millisecond))
	in.Handle("10.0.0.1", "dns done query: #4 dns name does not exist", t0.Add(300*time.Millisecond))
	in.Handle("10.0.0.1", "dns done query: #99 dns server failure", t0)
	in.Handle("10.0.0.1", "dhcp,info defconf assigned 192.168.88.37 for b0:e4:5c:27:ef:f2 Phone", t0)
	in.Handle("10.0.0.1", "interface,error ether1 link down", t0)

	in.flush(t0.Add(time.Second), false)
	got := map[string]store.Query{}
	for _, q := range sink.queries {
		got[q.Domain] = q
	}
	if len(got) != 3 {
		t.Fatalf("expected 3 completed queries, got %d: %+v", len(got), got)
	}
	if q := got["example.com"]; q.Outcome != "ok" || q.Answer != "93.184.216.34 93.184.216.35" || q.LatencyMS != 42 || q.Client != "192.168.88.10" {
		t.Errorf("example.com: %+v", q)
	}
	if q := got["ads.example.com"]; q.Outcome != "blocked" || q.Answer != "0.0.0.0" {
		t.Errorf("ads.example.com: %+v", q)
	}
	if q := got["missing.example"]; q.Outcome != "nxdomain" || q.Type != "AAAA" {
		t.Errorf("missing.example: %+v", q)
	}

	// The unanswered local query is released only after the timeout.
	sink.queries = nil
	in.flush(t0.Add(answerTimeout), false)
	if len(sink.queries) != 0 {
		t.Fatalf("query released before timeout: %+v", sink.queries)
	}
	in.flush(t0.Add(answerTimeout+time.Second), false)
	if len(sink.queries) != 1 || sink.queries[0].Domain != "cloud.mikrotik.com" || sink.queries[0].Outcome != "" || sink.queries[0].Client != "10.0.0.1" || sink.queries[0].LatencyMS != -1 {
		t.Errorf("timed out query: %+v", sink.queries)
	}

	if len(sink.events) != 2 || sink.events[1].Severity != "error" || sink.events[1].Topics != "interface,error" {
		t.Errorf("events: %+v", sink.events)
	}
	if len(sink.hosts) != 1 || sink.hosts[0].MAC != "B0:E4:5C:27:EF:F2" || sink.hosts[0].Name != "Phone" {
		t.Errorf("hosts: %+v", sink.hosts)
	}
	st := in.Stats()
	if st.Queries != 4 || st.Answered != 3 || st.Orphans != 1 || st.Events != 2 || st.Leases != 1 || st.Pending != 0 {
		t.Errorf("stats: %+v", st)
	}
}

func TestReusedIDReleasesOld(t *testing.T) {
	sink := &memSink{}
	in := New(sink, nil, slog.Default())
	t0 := time.Now()
	in.Handle("r", "dns query from 10.0.0.2: #7 a.example. A", t0)
	in.Handle("r", "dns query from 10.0.0.2: #7 b.example. A", t0.Add(time.Millisecond))
	if len(sink.queries) != 1 || sink.queries[0].Domain != "a.example" || sink.queries[0].Outcome != "" {
		t.Fatalf("expected the first query to be released unanswered, got %+v", sink.queries)
	}
}
