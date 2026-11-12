package store

import (
	"context"
	"database/sql"
	"errors"
	"sync"
	"time"
)

type MetricBucket struct {
	TS  int64    `json:"ts"`
	Avg *float64 `json:"avg"`
	Max *float64 `json:"max"`
	Min *float64 `json:"min"`
	N   int64    `json:"n"`
}

type Series struct {
	Key     string         `json:"key"`
	Buckets []MetricBucket `json:"buckets"`
}

// MetricTimeline returns zero-filled buckets per key for one metric. An
// empty key selects every key of the metric.
func (s *Store) MetricTimeline(ctx context.Context, metric, key string, since time.Time, bucket int64, now time.Time) ([]Series, error) {
	q := `SELECT key, (ts / ?) * ?, AVG(value), MAX(value), MIN(value), COUNT(*) FROM metrics WHERE metric = ? AND ts >= ?`
	args := []any{bucket, bucket, metric, since.Unix()}
	if key != "" {
		q += ` AND key = ?`
		args = append(args, key)
	}
	rows, err := s.ro.QueryContext(ctx, q+` GROUP BY 1, 2 ORDER BY 1, 2`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	byKey := map[string]map[int64]MetricBucket{}
	var order []string
	for rows.Next() {
		var k string
		var b MetricBucket
		var avg, max, min float64
		if err := rows.Scan(&k, &b.TS, &avg, &max, &min, &b.N); err != nil {
			return nil, err
		}
		b.Avg, b.Max, b.Min = &avg, &max, &min
		if _, ok := byKey[k]; !ok {
			byKey[k] = map[int64]MetricBucket{}
			order = append(order, k)
		}
		byKey[k][b.TS] = b
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	first := (since.Unix() / bucket) * bucket
	last := (now.Unix() / bucket) * bucket
	out := make([]Series, 0, len(order))
	for _, k := range order {
		ser := Series{Key: k, Buckets: make([]MetricBucket, 0, (last-first)/bucket+1)}
		for ts := first; ts <= last; ts += bucket {
			b, ok := byKey[k][ts]
			if !ok {
				b = MetricBucket{TS: ts}
			}
			ser.Buckets = append(ser.Buckets, b)
		}
		out = append(out, ser)
	}
	return out, nil
}

type Latest struct {
	Key   string  `json:"key"`
	TS    int64   `json:"ts"`
	Value float64 `json:"value"`
}

// MetricLatest returns the newest sample per key of a metric.
func (s *Store) MetricLatest(ctx context.Context, metric string, since time.Time) ([]Latest, error) {
	rows, err := s.ro.QueryContext(ctx, `SELECT key, ts, value FROM metrics m
		WHERE metric = ? AND ts >= ? AND ts = (SELECT MAX(ts) FROM metrics WHERE metric = m.metric AND key = m.key AND ts >= ?)
		ORDER BY key`, metric, since.Unix(), since.Unix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Latest{}
	for rows.Next() {
		var l Latest
		if err := rows.Scan(&l.Key, &l.TS, &l.Value); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

type Aggregate struct {
	Avg *float64 `json:"avg"`
	Max *float64 `json:"max"`
	Min *float64 `json:"min"`
	Sum *float64 `json:"sum"`
	N   int64    `json:"n"`
}

// MetricAggregate summarises one series over a window.
func (s *Store) MetricAggregate(ctx context.Context, metric, key string, since time.Time) (Aggregate, error) {
	var a Aggregate
	var avg, max, min, sum sql.NullFloat64
	err := s.ro.QueryRowContext(ctx, `SELECT AVG(value), MAX(value), MIN(value), SUM(value), COUNT(*) FROM metrics WHERE metric = ? AND key = ? AND ts >= ?`,
		metric, key, since.Unix()).Scan(&avg, &max, &min, &sum, &a.N)
	if err != nil {
		return a, err
	}
	for dst, src := range map[**float64]sql.NullFloat64{&a.Avg: avg, &a.Max: max, &a.Min: min, &a.Sum: sum} {
		if src.Valid {
			v := src.Float64
			*dst = &v
		}
	}
	return a, nil
}

// MetricKeys lists the keys seen for a metric in the window.
func (s *Store) MetricKeys(ctx context.Context, metric string, since time.Time) ([]string, error) {
	rows, err := s.ro.QueryContext(ctx, `SELECT DISTINCT key FROM metrics WHERE metric = ? AND ts >= ? ORDER BY key`, metric, since.Unix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			return nil, err
		}
		out = append(out, k)
	}
	return out, rows.Err()
}

func parallel(fns ...func() error) error {
	var wg sync.WaitGroup
	errs := make([]error, len(fns))
	for i, fn := range fns {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs[i] = fn()
		}()
	}
	wg.Wait()
	return errors.Join(errs...)
}
