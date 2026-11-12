package store

import (
	"database/sql"
	"fmt"
)

const schemaVersion = 1

// Each entry upgrades the database from the previous version. Entry 0 is
// the initial schema.
var migrations = [...][]string{
	{
		`CREATE TABLE dns_queries (
			id INTEGER PRIMARY KEY,
			ts INTEGER NOT NULL,
			router TEXT NOT NULL,
			client TEXT NOT NULL,
			domain TEXT NOT NULL,
			type TEXT NOT NULL,
			outcome TEXT NOT NULL DEFAULT '',
			answer TEXT NOT NULL DEFAULT '',
			latency_ms INTEGER)`,
		// Covering indexes: every dashboard aggregate is answered from an
		// index without touching table rows.
		`CREATE INDEX dns_queries_ts ON dns_queries(ts, outcome, type, latency_ms)`,
		`CREATE INDEX dns_queries_client ON dns_queries(client, ts, domain, outcome)`,
		`CREATE INDEX dns_queries_domain ON dns_queries(domain, ts, client, outcome)`,

		`CREATE TABLE hosts (
			ip TEXT PRIMARY KEY,
			mac TEXT NOT NULL DEFAULT '',
			name TEXT NOT NULL DEFAULT '',
			source TEXT NOT NULL DEFAULT '',
			updated INTEGER NOT NULL)`,
		`CREATE INDEX hosts_mac ON hosts(mac)`,

		`CREATE TABLE metrics (
			id INTEGER PRIMARY KEY,
			ts INTEGER NOT NULL,
			metric TEXT NOT NULL,
			key TEXT NOT NULL,
			value REAL NOT NULL)`,
		`CREATE INDEX metrics_series ON metrics(metric, key, ts, value)`,
		`CREATE INDEX metrics_ts ON metrics(ts)`,

		`CREATE TABLE events (
			id INTEGER PRIMARY KEY,
			ts INTEGER NOT NULL,
			router TEXT NOT NULL,
			topics TEXT NOT NULL,
			severity TEXT NOT NULL,
			message TEXT NOT NULL)`,
		`CREATE INDEX events_ts ON events(ts, severity)`,
	},
}

func migrate(db *sql.DB) error {
	var version int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		return err
	}
	if version > schemaVersion {
		return fmt.Errorf("database schema %d is newer than this binary (%d)", version, schemaVersion)
	}
	for v := version; v < schemaVersion; v++ {
		tx, err := db.Begin()
		if err != nil {
			return err
		}
		for _, stmt := range migrations[v] {
			if _, err := tx.Exec(stmt); err != nil {
				tx.Rollback()
				return fmt.Errorf("schema v%d: %w", v+1, err)
			}
		}
		if _, err := tx.Exec(fmt.Sprintf(`PRAGMA user_version = %d`, v+1)); err != nil {
			tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}
