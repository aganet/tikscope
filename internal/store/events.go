package store

import (
	"context"
	"strings"
	"time"
)

type EventFilter struct {
	Since    time.Time
	Until    time.Time // zero means open-ended
	Severity string    // info, warning, error, critical, or "problem" for warning and above
	Search   string    // substring of topics or message
}

type EventRow struct {
	ID       int64  `json:"id"`
	TS       int64  `json:"ts"`
	Router   string `json:"router"`
	Topics   string `json:"topics"`
	Severity string `json:"severity"`
	Message  string `json:"message"`
}

func (f EventFilter) where() (string, []any) {
	clauses := []string{"ts >= ?"}
	args := []any{f.Since.Unix()}
	if !f.Until.IsZero() {
		clauses = append(clauses, "ts <= ?")
		args = append(args, f.Until.Unix())
	}
	switch f.Severity {
	case "":
	case "problem":
		clauses = append(clauses, "severity IN ('warning','error','critical')")
	default:
		clauses = append(clauses, "severity = ?")
		args = append(args, f.Severity)
	}
	if f.Search != "" {
		pat := "%" + escapeLike(strings.ToLower(f.Search)) + "%"
		clauses = append(clauses, `(LOWER(message) LIKE ? ESCAPE '\' OR topics LIKE ? ESCAPE '\')`)
		args = append(args, pat, pat)
	}
	return " WHERE " + strings.Join(clauses, " AND "), args
}

// Events returns one page of events, newest first, with the total.
func (s *Store) Events(ctx context.Context, f EventFilter, page, size int) ([]EventRow, int64, error) {
	where, args := f.where()
	var total int64
	items := []EventRow{}
	err := parallel(
		func() error {
			return s.ro.QueryRowContext(ctx, `SELECT COUNT(*) FROM events`+where, args...).Scan(&total)
		},
		func() error {
			rows, err := s.ro.QueryContext(ctx, `SELECT id, ts, router, topics, severity, message FROM events`+where+
				` ORDER BY ts DESC, id DESC LIMIT ? OFFSET ?`, append(args, size, (page-1)*size)...)
			if err != nil {
				return err
			}
			defer rows.Close()
			for rows.Next() {
				var e EventRow
				if err := rows.Scan(&e.ID, &e.TS, &e.Router, &e.Topics, &e.Severity, &e.Message); err != nil {
					return err
				}
				items = append(items, e)
			}
			return rows.Err()
		},
	)
	return items, total, err
}

// EventCounts groups the window by severity.
func (s *Store) EventCounts(ctx context.Context, since, until time.Time) (map[string]int64, error) {
	rows, err := s.ro.QueryContext(ctx, `SELECT severity, COUNT(*) FROM events WHERE ts >= ? AND ts <= ? GROUP BY 1`, since.Unix(), until.Unix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int64{}
	for rows.Next() {
		var sev string
		var n int64
		if err := rows.Scan(&sev, &n); err != nil {
			return nil, err
		}
		out[sev] = n
	}
	return out, rows.Err()
}

// Hosts lists every known device identity.
func (s *Store) Hosts(ctx context.Context) ([]Host, error) {
	rows, err := s.ro.QueryContext(ctx, `SELECT ip, mac, name, source FROM hosts ORDER BY ip`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Host{}
	for rows.Next() {
		var h Host
		if err := rows.Scan(&h.IP, &h.MAC, &h.Name, &h.Source); err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
}
