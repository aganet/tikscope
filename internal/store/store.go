// Package store owns the SQLite database: schema, a single batching writer
// and every read the API needs.
package store

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"time"

	_ "modernc.org/sqlite"
)

// Query is one DNS request as logged by the router.
type Query struct {
	TS        time.Time
	Router    string
	Client    string
	Domain    string
	Type      string
	Outcome   string
	Answer    string
	LatencyMS int64 // -1 when unknown
}

// Host maps a client address to a device identity.
type Host struct {
	IP     string
	MAC    string
	Name   string
	Source string
}

// Event is a router log line that is not a DNS query, or a notice the
// collector raised itself (probe state changes).
type Event struct {
	TS       time.Time
	Router   string
	Topics   string
	Severity string
	Message  string
}

// Sample is one time-series point. Metric names are dotted
// ("iface.rx_bps"), Key identifies the series within a metric.
type Sample struct {
	TS     time.Time
	Metric string
	Key    string
	Value  float64
}

type Store struct {
	path    string
	rw      *sql.DB
	ro      *sql.DB
	queries chan Query
	hosts   chan Host
	events  chan Event
	samples chan Sample
	dropped atomic.Int64
	written atomic.Int64
	log     *slog.Logger
}

const (
	batchSize  = 2000
	flushEvery = time.Second
	purgeEvery = 10 * time.Minute
	purgeChunk = 20000
)

func Open(path string, log *slog.Logger) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	dsn := "file:" + path + "?_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=busy_timeout(5000)&_pragma=cache_size(-32000)&_pragma=temp_store(MEMORY)"
	rw, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	rw.SetMaxOpenConns(1)
	if err := migrate(rw); err != nil {
		rw.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	ro, err := sql.Open("sqlite", dsn+"&_pragma=query_only(1)")
	if err != nil {
		rw.Close()
		return nil, err
	}
	ro.SetMaxOpenConns(runtime.NumCPU())
	return &Store{
		path:    path,
		rw:      rw,
		ro:      ro,
		queries: make(chan Query, 16384),
		hosts:   make(chan Host, 1024),
		events:  make(chan Event, 4096),
		samples: make(chan Sample, 16384),
		log:     log,
	}, nil
}

func (s *Store) Close() error {
	s.ro.Close()
	return s.rw.Close()
}

// The Add methods never block the producers; a full queue drops and counts.
func (s *Store) AddQuery(q Query) { enqueue(s, s.queries, q) }
func (s *Store) AddHost(h Host)   { enqueue(s, s.hosts, h) }
func (s *Store) AddEvent(e Event) { enqueue(s, s.events, e) }
func (s *Store) AddSample(m Sample) {
	enqueue(s, s.samples, m)
}

func enqueue[T any](s *Store, ch chan T, v T) {
	select {
	case ch <- v:
	default:
		s.dropped.Add(1)
	}
}

// Retention controls how long each kind of row is kept.
type Retention struct {
	Queries time.Duration
	Metrics time.Duration
	Events  time.Duration
}

// Run owns all writes. It returns after ctx is cancelled and the queues
// are flushed.
func (s *Store) Run(ctx context.Context, ret Retention) {
	flush := time.NewTicker(flushEvery)
	purge := time.NewTicker(purgeEvery)
	defer flush.Stop()
	defer purge.Stop()

	s.purge(ret)
	var (
		queries = make([]Query, 0, batchSize)
		hosts   []Host
		events  []Event
		samples = make([]Sample, 0, batchSize)
	)
	flushAll := func() {
		s.insertQueries(queries)
		s.insertSamples(samples)
		s.insertEvents(events)
		s.upsertHosts(hosts)
		queries, samples, events, hosts = queries[:0], samples[:0], events[:0], hosts[:0]
	}
	for {
		select {
		case q := <-s.queries:
			queries = append(queries, q)
			if len(queries) >= batchSize {
				s.insertQueries(queries)
				queries = queries[:0]
			}
		case m := <-s.samples:
			samples = append(samples, m)
			if len(samples) >= batchSize {
				s.insertSamples(samples)
				samples = samples[:0]
			}
		case e := <-s.events:
			events = append(events, e)
		case h := <-s.hosts:
			hosts = append(hosts, h)
		case <-flush.C:
			flushAll()
		case <-purge.C:
			s.purge(ret)
		case <-ctx.Done():
			for len(s.queries) > 0 {
				queries = append(queries, <-s.queries)
			}
			for len(s.samples) > 0 {
				samples = append(samples, <-s.samples)
			}
			for len(s.events) > 0 {
				events = append(events, <-s.events)
			}
			for len(s.hosts) > 0 {
				hosts = append(hosts, <-s.hosts)
			}
			flushAll()
			return
		}
	}
}

// tx runs fn inside one write transaction and logs rather than returns
// failures, since the writer loop has nobody to report to.
func (s *Store) tx(what string, fn func(tx *sql.Tx) error) bool {
	tx, err := s.rw.Begin()
	if err != nil {
		s.log.Error("begin "+what, "err", err)
		return false
	}
	if err := fn(tx); err != nil {
		tx.Rollback()
		s.log.Error(what, "err", err)
		return false
	}
	if err := tx.Commit(); err != nil {
		s.log.Error("commit "+what, "err", err)
		return false
	}
	return true
}

func (s *Store) insertQueries(batch []Query) {
	if len(batch) == 0 {
		return
	}
	ok := s.tx("insert queries", func(tx *sql.Tx) error {
		stmt, err := tx.Prepare(`INSERT INTO dns_queries(ts, router, client, domain, type, outcome, answer, latency_ms) VALUES(?,?,?,?,?,?,?,?)`)
		if err != nil {
			return err
		}
		defer stmt.Close()
		for _, q := range batch {
			var latency any
			if q.LatencyMS >= 0 {
				latency = q.LatencyMS
			}
			if _, err := stmt.Exec(q.TS.Unix(), q.Router, q.Client, q.Domain, q.Type, q.Outcome, q.Answer, latency); err != nil {
				return err
			}
		}
		return nil
	})
	if ok {
		s.written.Add(int64(len(batch)))
	}
}

func (s *Store) insertSamples(batch []Sample) {
	if len(batch) == 0 {
		return
	}
	ok := s.tx("insert samples", func(tx *sql.Tx) error {
		stmt, err := tx.Prepare(`INSERT INTO metrics(ts, metric, key, value) VALUES(?,?,?,?)`)
		if err != nil {
			return err
		}
		defer stmt.Close()
		for _, m := range batch {
			if _, err := stmt.Exec(m.TS.Unix(), m.Metric, m.Key, m.Value); err != nil {
				return err
			}
		}
		return nil
	})
	if ok {
		s.written.Add(int64(len(batch)))
	}
}

func (s *Store) insertEvents(batch []Event) {
	if len(batch) == 0 {
		return
	}
	ok := s.tx("insert events", func(tx *sql.Tx) error {
		stmt, err := tx.Prepare(`INSERT INTO events(ts, router, topics, severity, message) VALUES(?,?,?,?,?)`)
		if err != nil {
			return err
		}
		defer stmt.Close()
		for _, e := range batch {
			if _, err := stmt.Exec(e.TS.Unix(), e.Router, e.Topics, e.Severity, e.Message); err != nil {
				return err
			}
		}
		return nil
	})
	if ok {
		s.written.Add(int64(len(batch)))
	}
}

func (s *Store) upsertHosts(hosts []Host) {
	if len(hosts) == 0 {
		return
	}
	now := time.Now().Unix()
	s.tx("upsert hosts", func(tx *sql.Tx) error {
		for _, h := range hosts {
			_, err := tx.Exec(`INSERT INTO hosts(ip, mac, name, source, updated) VALUES(?,?,?,?,?)
				ON CONFLICT(ip) DO UPDATE SET
					mac = CASE WHEN excluded.mac <> '' THEN excluded.mac ELSE hosts.mac END,
					name = CASE WHEN excluded.name <> '' THEN excluded.name ELSE hosts.name END,
					source = excluded.source,
					updated = excluded.updated`,
				h.IP, h.MAC, h.Name, h.Source, now)
			if err != nil {
				return err
			}
		}
		// Addresses known only by MAC (IPv6 neighbours, ARP) inherit the
		// name of the DHCP lease on the same hardware.
		_, err := tx.Exec(`UPDATE hosts SET name = (
				SELECT h2.name FROM hosts h2 WHERE h2.mac = hosts.mac AND h2.name <> '' ORDER BY h2.updated DESC LIMIT 1)
			WHERE name = '' AND mac <> ''
			  AND EXISTS (SELECT 1 FROM hosts h3 WHERE h3.mac = hosts.mac AND h3.name <> '')`)
		return err
	})
}

func (s *Store) purge(ret Retention) {
	for _, p := range []struct {
		table string
		keep  time.Duration
	}{{"dns_queries", ret.Queries}, {"metrics", ret.Metrics}, {"events", ret.Events}} {
		cutoff := time.Now().Add(-p.keep).Unix()
		var total int64
		for {
			res, err := s.rw.Exec(`DELETE FROM `+p.table+` WHERE id IN (SELECT id FROM `+p.table+` WHERE ts < ? LIMIT ?)`, cutoff, purgeChunk)
			if err != nil {
				s.log.Error("purge", "table", p.table, "err", err)
				break
			}
			n, _ := res.RowsAffected()
			total += n
			if n < purgeChunk {
				break
			}
		}
		if total > 0 {
			s.log.Info("purged", "table", p.table, "rows", total)
		}
	}
	if _, err := s.rw.Exec(`PRAGMA optimize`); err != nil {
		s.log.Warn("optimize", "err", err)
	}
}

// Size reports the on-disk footprint including the WAL.
func (s *Store) Size() int64 {
	var total int64
	for _, suffix := range []string{"", "-wal"} {
		if fi, err := os.Stat(s.path + suffix); err == nil {
			total += fi.Size()
		}
	}
	return total
}

func (s *Store) Stats() (written, dropped int64) {
	return s.written.Load(), s.dropped.Load()
}

// escapeLike makes a user string safe inside a LIKE pattern with ESCAPE '\'.
func escapeLike(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}
