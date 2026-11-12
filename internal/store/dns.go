package store

import (
	"context"
	"database/sql"
	"encoding/csv"
	"errors"
	"io"
	"sort"
	"strconv"
	"strings"
	"time"
)

// DNSFilter narrows every DNS read to a time window and optional dimensions.
type DNSFilter struct {
	Since   time.Time
	Client  string
	Domain  string // exact
	Search  string // substring of the domain
	Type    string
	Outcome string // stored outcome or a group: resolved, problem, failed, unknown
}

const problemOutcomes = `('nxdomain','blocked','servfail','error')`

func (f DNSFilter) where() (string, []any) {
	clauses := []string{"ts >= ?"}
	args := []any{f.Since.Unix()}
	if f.Client != "" {
		clauses = append(clauses, "client = ?")
		args = append(args, f.Client)
	}
	if f.Domain != "" {
		clauses = append(clauses, "domain = ?")
		args = append(args, strings.ToLower(f.Domain))
	}
	if f.Search != "" {
		clauses = append(clauses, `domain LIKE ? ESCAPE '\'`)
		args = append(args, "%"+escapeLike(strings.ToLower(f.Search))+"%")
	}
	if f.Type != "" {
		clauses = append(clauses, "type = ?")
		args = append(args, strings.ToUpper(f.Type))
	}
	switch f.Outcome {
	case "":
	case "resolved":
		clauses = append(clauses, "outcome IN ('ok','nodata')")
	case "failed":
		clauses = append(clauses, "outcome IN ('servfail','error')")
	case "problem":
		clauses = append(clauses, "outcome IN "+problemOutcomes)
	case "unknown":
		clauses = append(clauses, "outcome = ''")
	default:
		clauses = append(clauses, "outcome = ?")
		args = append(args, f.Outcome)
	}
	return " WHERE " + strings.Join(clauses, " AND "), args
}

type Count struct {
	Key   string `json:"key"`
	Count int64  `json:"count"`
}

type DNSBucket struct {
	TS        int64    `json:"ts"`
	Total     int64    `json:"total"`
	Resolved  int64    `json:"resolved"`
	NXDomain  int64    `json:"nxdomain"`
	Blocked   int64    `json:"blocked"`
	Failed    int64    `json:"failed"`
	Unknown   int64    `json:"unknown"`
	LatencyMS *float64 `json:"latency_ms"`
}

type DomainStat struct {
	Domain   string `json:"domain"`
	Count    int64  `json:"count"`
	Clients  int64  `json:"clients"`
	Problems int64  `json:"problems"`
	LastSeen int64  `json:"last_seen"`
}

type ClientStat struct {
	Client   string `json:"client"`
	Name     string `json:"name"`
	MAC      string `json:"mac"`
	Count    int64  `json:"count"`
	Domains  int64  `json:"domains"`
	Problems int64  `json:"problems"`
	LastSeen int64  `json:"last_seen"`
}

type QueryRow struct {
	ID        int64  `json:"id"`
	TS        int64  `json:"ts"`
	Router    string `json:"router"`
	Client    string `json:"client"`
	Name      string `json:"name"`
	Domain    string `json:"domain"`
	Type      string `json:"type"`
	Outcome   string `json:"outcome"`
	Answer    string `json:"answer"`
	LatencyMS *int64 `json:"latency_ms"`
}

type DNSTotals struct {
	Queries   int64            `json:"queries"`
	Clients   int64            `json:"clients"`
	Domains   int64            `json:"domains"`
	IPv6      int64            `json:"ipv6"`
	Outcomes  map[string]int64 `json:"outcomes"`
	LatencyMS *float64         `json:"latency_ms"`
}

type DNSSummary struct {
	Since      int64        `json:"since"`
	Until      int64        `json:"until"`
	BucketSecs int64        `json:"bucket_secs"`
	Totals     DNSTotals    `json:"totals"`
	Types      []Count      `json:"types"`
	Timeline   []DNSBucket  `json:"timeline"`
	TopDomains []DomainStat `json:"top_domains"`
	TopClients []ClientStat `json:"top_clients"`
	Problems   []DomainStat `json:"problems"`
}

// BucketFor picks a timeline resolution that keeps a range under ~300 points.
func BucketFor(window time.Duration) int64 {
	switch {
	case window <= 2*time.Hour:
		return 60
	case window <= 12*time.Hour:
		return 300
	case window <= 48*time.Hour:
		return 900
	case window <= 8*24*time.Hour:
		return 3600
	default:
		return 6 * 3600
	}
}

func (s *Store) DNSSummary(ctx context.Context, window time.Duration, top int) (*DNSSummary, error) {
	now := time.Now()
	f := DNSFilter{Since: now.Add(-window)}
	out := &DNSSummary{Since: f.Since.Unix(), Until: now.Unix(), BucketSecs: BucketFor(window)}
	err := parallel(
		func() (err error) { out.Totals, out.Types, err = s.dnsTotals(ctx, f); return },
		func() (err error) { out.Timeline, err = s.DNSTimeline(ctx, f, out.BucketSecs, now); return },
		func() (err error) { out.TopDomains, err = s.Domains(ctx, f, top); return },
		func() (err error) { out.TopClients, err = s.Clients(ctx, f, top); return },
		func() (err error) {
			pf := f
			pf.Outcome = "problem"
			out.Problems, err = s.Domains(ctx, pf, top)
			return
		},
	)
	return out, err
}

func (s *Store) dnsTotals(ctx context.Context, f DNSFilter) (DNSTotals, []Count, error) {
	where, args := f.where()
	t := DNSTotals{Outcomes: map[string]int64{}}
	var latency sql.NullFloat64
	if err := s.ro.QueryRowContext(ctx, `SELECT COUNT(*), AVG(latency_ms) FROM dns_queries`+where, args...).Scan(&t.Queries, &latency); err != nil {
		return t, nil, err
	}
	if latency.Valid {
		t.LatencyMS = &latency.Float64
	}
	// Grouping walks the client and domain indexes in order, so distinct
	// counts need no temporary sort.
	if err := s.ro.QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(SUM(INSTR(client, ':') > 0), 0)
		FROM (SELECT client FROM dns_queries`+where+` GROUP BY client)`, args...).Scan(&t.Clients, &t.IPv6); err != nil {
		return t, nil, err
	}
	if err := s.ro.QueryRowContext(ctx, `SELECT COUNT(*) FROM (SELECT 1 FROM dns_queries`+where+` GROUP BY domain)`, args...).Scan(&t.Domains); err != nil {
		return t, nil, err
	}
	rows, err := s.ro.QueryContext(ctx, `SELECT type, outcome, COUNT(*) FROM dns_queries`+where+` GROUP BY 1, 2`, args...)
	if err != nil {
		return t, nil, err
	}
	defer rows.Close()
	byType := map[string]int64{}
	for rows.Next() {
		var typ, outcome string
		var n int64
		if err := rows.Scan(&typ, &outcome, &n); err != nil {
			return t, nil, err
		}
		byType[typ] += n
		t.Outcomes[outcome] += n
	}
	types := make([]Count, 0, len(byType))
	for k, n := range byType {
		types = append(types, Count{Key: k, Count: n})
	}
	sort.Slice(types, func(i, j int) bool {
		if types[i].Count != types[j].Count {
			return types[i].Count > types[j].Count
		}
		return types[i].Key < types[j].Key
	})
	return t, types, rows.Err()
}

// DNSTimeline returns one bucket per interval from f.Since to now, zero-filled.
func (s *Store) DNSTimeline(ctx context.Context, f DNSFilter, bucket int64, now time.Time) ([]DNSBucket, error) {
	where, args := f.where()
	rows, err := s.ro.QueryContext(ctx, `SELECT (ts / ?) * ?, COUNT(*),
		SUM(outcome IN ('ok','nodata')), SUM(outcome = 'nxdomain'), SUM(outcome = 'blocked'),
		SUM(outcome IN ('servfail','error')), SUM(outcome = ''), AVG(latency_ms)
		FROM dns_queries`+where+` GROUP BY 1 ORDER BY 1`, append([]any{bucket, bucket}, args...)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	byTS := map[int64]DNSBucket{}
	for rows.Next() {
		var b DNSBucket
		var latency sql.NullFloat64
		if err := rows.Scan(&b.TS, &b.Total, &b.Resolved, &b.NXDomain, &b.Blocked, &b.Failed, &b.Unknown, &latency); err != nil {
			return nil, err
		}
		if latency.Valid {
			b.LatencyMS = &latency.Float64
		}
		byTS[b.TS] = b
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	first := (f.Since.Unix() / bucket) * bucket
	last := (now.Unix() / bucket) * bucket
	out := make([]DNSBucket, 0, (last-first)/bucket+1)
	for ts := first; ts <= last; ts += bucket {
		b, ok := byTS[ts]
		if !ok {
			b = DNSBucket{TS: ts}
		}
		out = append(out, b)
	}
	return out, nil
}

// Domains ranks domains by query count; distinct clients and problems are
// added in a second pass for the returned rows only.
func (s *Store) Domains(ctx context.Context, f DNSFilter, limit int) ([]DomainStat, error) {
	where, args := f.where()
	rows, err := s.ro.QueryContext(ctx, `SELECT domain, COUNT(*) AS n, MAX(ts)
		FROM dns_queries`+where+` GROUP BY domain ORDER BY n DESC, domain LIMIT ?`, append(args, limit)...)
	if err != nil {
		return nil, err
	}
	out := []DomainStat{}
	for rows.Next() {
		var d DomainStat
		if err := rows.Scan(&d.Domain, &d.Count, &d.LastSeen); err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, d)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range out {
		err := s.ro.QueryRowContext(ctx, `SELECT COUNT(DISTINCT client), SUM(outcome IN `+problemOutcomes+`)
			FROM dns_queries`+where+` AND domain = ?`, append(args, out[i].Domain)...).Scan(&out[i].Clients, &out[i].Problems)
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

func (s *Store) Clients(ctx context.Context, f DNSFilter, limit int) ([]ClientStat, error) {
	where, args := f.where()
	rows, err := s.ro.QueryContext(ctx, `SELECT client, COUNT(*) AS n, MAX(ts)
		FROM dns_queries`+where+` GROUP BY client ORDER BY n DESC, client LIMIT ?`, append(args, limit)...)
	if err != nil {
		return nil, err
	}
	out := []ClientStat{}
	for rows.Next() {
		var c ClientStat
		if err := rows.Scan(&c.Client, &c.Count, &c.LastSeen); err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, c)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range out {
		c := &out[i]
		err := s.ro.QueryRowContext(ctx, `SELECT COUNT(DISTINCT domain), SUM(outcome IN `+problemOutcomes+`)
			FROM dns_queries`+where+` AND client = ?`, append(args, c.Client)...).Scan(&c.Domains, &c.Problems)
		if err != nil {
			return nil, err
		}
		c.Name, c.MAC, err = s.hostIdentity(ctx, c.Client)
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

func (s *Store) hostIdentity(ctx context.Context, ip string) (name, mac string, err error) {
	err = s.ro.QueryRowContext(ctx, `SELECT name, mac FROM hosts WHERE ip = ?`, ip).Scan(&name, &mac)
	if errors.Is(err, sql.ErrNoRows) {
		err = nil
	}
	return
}

const queryColumns = `q.id, q.ts, q.router, q.client, COALESCE(h.name, ''), q.domain, q.type, q.outcome, q.answer, q.latency_ms`

func scanQuery(rows *sql.Rows) (QueryRow, error) {
	var r QueryRow
	var latency sql.NullInt64
	err := rows.Scan(&r.ID, &r.TS, &r.Router, &r.Client, &r.Name, &r.Domain, &r.Type, &r.Outcome, &r.Answer, &latency)
	if latency.Valid {
		r.LatencyMS = &latency.Int64
	}
	return r, err
}

// Queries returns one page of matching rows, newest first, and the total match count.
func (s *Store) Queries(ctx context.Context, f DNSFilter, page, size int) ([]QueryRow, int64, error) {
	where, args := f.where()
	var total int64
	items := []QueryRow{}
	err := parallel(
		func() error {
			return s.ro.QueryRowContext(ctx, `SELECT COUNT(*) FROM dns_queries`+where, args...).Scan(&total)
		},
		func() error {
			rows, err := s.ro.QueryContext(ctx, `SELECT `+queryColumns+` FROM (SELECT * FROM dns_queries`+where+
				` ORDER BY ts DESC, id DESC LIMIT ? OFFSET ?) q LEFT JOIN hosts h ON h.ip = q.client ORDER BY q.ts DESC, q.id DESC`,
				append(args, size, (page-1)*size)...)
			if err != nil {
				return err
			}
			defer rows.Close()
			for rows.Next() {
				r, err := scanQuery(rows)
				if err != nil {
					return err
				}
				items = append(items, r)
			}
			return rows.Err()
		},
	)
	return items, total, err
}

// ExportCSV streams every matching row, newest first.
func (s *Store) ExportCSV(ctx context.Context, f DNSFilter, w io.Writer) error {
	where, args := f.where()
	rows, err := s.ro.QueryContext(ctx, `SELECT `+queryColumns+` FROM dns_queries q LEFT JOIN hosts h ON h.ip = q.client`+
		where+` ORDER BY q.ts DESC, q.id DESC`, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	cw := csv.NewWriter(w)
	if err := cw.Write([]string{"time", "router", "client", "device", "domain", "type", "outcome", "answer", "latency_ms"}); err != nil {
		return err
	}
	for rows.Next() {
		r, err := scanQuery(rows)
		if err != nil {
			return err
		}
		latency := ""
		if r.LatencyMS != nil {
			latency = strconv.FormatInt(*r.LatencyMS, 10)
		}
		rec := []string{time.Unix(r.TS, 0).UTC().Format(time.RFC3339), r.Router, r.Client, r.Name, r.Domain, r.Type, r.Outcome, r.Answer, latency}
		if err := cw.Write(rec); err != nil {
			return err
		}
	}
	cw.Flush()
	return errors.Join(rows.Err(), cw.Error())
}

// DNSTotalsSince is the totals part of the summary for an arbitrary window.
func (s *Store) DNSTotalsSince(ctx context.Context, since time.Time) (DNSTotals, []Count, error) {
	return s.dnsTotals(ctx, DNSFilter{Since: since})
}
