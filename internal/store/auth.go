package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

var (
	ErrNotFound   = errors.New("not found")
	ErrSetupDone  = errors.New("an account already exists")
	ErrUserExists = errors.New("username already taken")
)

type User struct {
	ID              int64
	Username        string
	PasswordHash    string
	Created         time.Time
	PasswordChanged time.Time
}

type Session struct {
	UserID   int64
	Username string
	Created  time.Time
	Expires  time.Time
	LastSeen time.Time
	IP       string
	Agent    string
}

func (s *Store) UserCount(ctx context.Context) (int, error) {
	var n int
	err := s.ro.QueryRowContext(ctx, `SELECT count(*) FROM users`).Scan(&n)
	return n, err
}

// CreateFirstUser registers the initial account and fails once any
// account exists, so the setup page cannot be reused.
func (s *Store) CreateFirstUser(ctx context.Context, username, hash string) error {
	now := time.Now().Unix()
	res, err := s.rw.ExecContext(ctx, `INSERT INTO users(username, password_hash, created, password_changed)
		SELECT ?, ?, ?, ? WHERE NOT EXISTS (SELECT 1 FROM users)`, username, hash, now, now)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrSetupDone
	}
	return nil
}

// UpsertUser creates the account or replaces its password, dropping every
// session of that user. It backs the set-password command.
func (s *Store) UpsertUser(ctx context.Context, username, hash string) error {
	now := time.Now().Unix()
	tx, err := s.rw.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `INSERT INTO users(username, password_hash, created, password_changed) VALUES(?,?,?,?)
		ON CONFLICT(username) DO UPDATE SET password_hash = excluded.password_hash, password_changed = excluded.password_changed`,
		username, hash, now, now); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM sessions WHERE user_id = (SELECT id FROM users WHERE username = ?)`, username); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) UserByName(ctx context.Context, username string) (User, error) {
	var u User
	var created, changed int64
	err := s.ro.QueryRowContext(ctx, `SELECT id, username, password_hash, created, password_changed FROM users WHERE username = ?`, username).
		Scan(&u.ID, &u.Username, &u.PasswordHash, &created, &changed)
	if errors.Is(err, sql.ErrNoRows) {
		return u, ErrNotFound
	}
	u.Created, u.PasswordChanged = time.Unix(created, 0), time.Unix(changed, 0)
	return u, err
}

func (s *Store) SetPassword(ctx context.Context, userID int64, hash string) error {
	_, err := s.rw.ExecContext(ctx, `UPDATE users SET password_hash = ?, password_changed = ? WHERE id = ?`, hash, time.Now().Unix(), userID)
	return err
}

func (s *Store) CreateSession(ctx context.Context, tokenHash []byte, userID int64, expires time.Time, ip, agent string) error {
	now := time.Now().Unix()
	_, err := s.rw.ExecContext(ctx, `INSERT INTO sessions(token_hash, user_id, created, expires, last_seen, ip, agent) VALUES(?,?,?,?,?,?,?)`,
		tokenHash, userID, now, expires.Unix(), now, ip, agent)
	return err
}

// SessionByToken returns the session for a token hash unless it has expired.
func (s *Store) SessionByToken(ctx context.Context, tokenHash []byte, now time.Time) (Session, error) {
	var sess Session
	var created, expires, seen int64
	err := s.ro.QueryRowContext(ctx, `SELECT s.user_id, u.username, s.created, s.expires, s.last_seen, s.ip, s.agent
		FROM sessions s JOIN users u ON u.id = s.user_id WHERE s.token_hash = ? AND s.expires > ?`, tokenHash, now.Unix()).
		Scan(&sess.UserID, &sess.Username, &created, &expires, &seen, &sess.IP, &sess.Agent)
	if errors.Is(err, sql.ErrNoRows) {
		return sess, ErrNotFound
	}
	sess.Created, sess.Expires, sess.LastSeen = time.Unix(created, 0), time.Unix(expires, 0), time.Unix(seen, 0)
	return sess, err
}

func (s *Store) TouchSession(ctx context.Context, tokenHash []byte, now time.Time) error {
	_, err := s.rw.ExecContext(ctx, `UPDATE sessions SET last_seen = ? WHERE token_hash = ?`, now.Unix(), tokenHash)
	return err
}

func (s *Store) DeleteSession(ctx context.Context, tokenHash []byte) error {
	_, err := s.rw.ExecContext(ctx, `DELETE FROM sessions WHERE token_hash = ?`, tokenHash)
	return err
}

// DeleteUserSessions removes every session of the user except keep, which
// may be nil to sign out everywhere.
func (s *Store) DeleteUserSessions(ctx context.Context, userID int64, keep []byte) error {
	_, err := s.rw.ExecContext(ctx, `DELETE FROM sessions WHERE user_id = ? AND (? IS NULL OR token_hash <> ?)`, userID, keep, keep)
	return err
}

func (s *Store) PurgeSessions(ctx context.Context, now time.Time) error {
	_, err := s.rw.ExecContext(ctx, `DELETE FROM sessions WHERE expires <= ?`, now.Unix())
	return err
}
