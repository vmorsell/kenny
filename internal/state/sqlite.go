// Package state persists Kenny's episodic memory in SQLite at /state/kenny.db.
// Survives container rebirth; cleared only if the persistent volume is wiped.
package state

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	_ "modernc.org/sqlite"
)

type Store struct {
	db *sql.DB
}

type JournalEntry struct {
	ID      int64
	LifeID  int64
	At      time.Time
	Kind    string
	Message string
}

type InflightTask struct {
	ID        int64
	LifeID    int64
	StartedAt time.Time
	Kind      string
	Payload   string
	Status    string
}

// Open opens or creates the SQLite database at path and runs migrations.
// Use path ":memory:" for ephemeral tests.
func Open(ctx context.Context, path string) (*Store, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	// SQLite with modernc works best with a small connection pool
	// because the driver serializes anyway.
	db.SetMaxOpenConns(1)
	s := &Store{db: db}
	if err := s.migrate(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() error { return s.db.Close() }

// Ping verifies the database is reachable. Used by /healthz.
func (s *Store) Ping(ctx context.Context) error {
	return s.db.PingContext(ctx)
}

func (s *Store) migrate(ctx context.Context) error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS metadata (
			key   TEXT PRIMARY KEY,
			value TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS journal (
			id       INTEGER PRIMARY KEY AUTOINCREMENT,
			life_id  INTEGER NOT NULL,
			at       DATETIME NOT NULL,
			kind     TEXT NOT NULL,
			message  TEXT NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS journal_life_idx ON journal(life_id)`,
		`CREATE INDEX IF NOT EXISTS journal_at_idx ON journal(at)`,
		`CREATE TABLE IF NOT EXISTS inflight (
			id         INTEGER PRIMARY KEY AUTOINCREMENT,
			life_id    INTEGER NOT NULL,
			started_at DATETIME NOT NULL,
			kind       TEXT NOT NULL,
			payload    TEXT NOT NULL,
			status     TEXT NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS inflight_status_idx ON inflight(status)`,
		`CREATE TABLE IF NOT EXISTS sessions (
			purpose      TEXT PRIMARY KEY,
			session_id   TEXT NOT NULL,
			created_at   DATETIME NOT NULL,
			last_used_at DATETIME NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS secrets (
			key        TEXT PRIMARY KEY,
			value      TEXT NOT NULL,
			created_at DATETIME NOT NULL,
			updated_at DATETIME NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS messages (
			id          INTEGER PRIMARY KEY AUTOINCREMENT,
			received_at DATETIME NOT NULL,
			content     TEXT NOT NULL,
			consumed_at DATETIME
		)`,
	}
	for _, stmt := range stmts {
		if _, err := s.db.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("migrate: %s: %w", stmt, err)
		}
	}
	return nil
}

// ------------------- Metadata -------------------

func (s *Store) GetMetadata(ctx context.Context, key string) (string, bool, error) {
	var v string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM metadata WHERE key = ?`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return v, true, nil
}

func (s *Store) SetMetadata(ctx context.Context, key, value string) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO metadata (key, value) VALUES (?, ?)
		 ON CONFLICT(key) DO UPDATE SET value = excluded.value`,
		key, value)
	return err
}

// ------------------- Life -------------------

// BeginLife increments the persistent boot counter and returns the new value.
// The returned value is Kenny's life ID for this incarnation.
func (s *Store) BeginLife(ctx context.Context) (int64, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	var current int64
	var raw string
	err = tx.QueryRowContext(ctx, `SELECT value FROM metadata WHERE key = 'boot_count'`).Scan(&raw)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		current = 0
	case err != nil:
		return 0, err
	default:
		_, err = fmt.Sscanf(raw, "%d", &current)
		if err != nil {
			return 0, fmt.Errorf("parse boot_count: %w", err)
		}
	}
	next := current + 1
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO metadata (key, value) VALUES ('boot_count', ?)
		 ON CONFLICT(key) DO UPDATE SET value = excluded.value`,
		fmt.Sprintf("%d", next)); err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return next, nil
}

// ------------------- Journal -------------------

func (s *Store) AppendJournal(ctx context.Context, lifeID int64, kind, message string) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO journal (life_id, at, kind, message) VALUES (?, ?, ?, ?)`,
		lifeID, time.Now().UTC(), kind, message)
	return err
}

// RecentJournal returns the most recent journal entries, newest first.
// If lifeID > 0 only entries for that life are returned.
func (s *Store) RecentJournal(ctx context.Context, limit int, lifeID ...int64) ([]JournalEntry, error) {
	var rows *sql.Rows
	var err error
	if len(lifeID) > 0 && lifeID[0] > 0 {
		rows, err = s.db.QueryContext(ctx,
			`SELECT id, life_id, at, kind, message FROM journal WHERE life_id = ? ORDER BY id DESC LIMIT ?`,
			lifeID[0], limit)
	} else {
		rows, err = s.db.QueryContext(ctx,
			`SELECT id, life_id, at, kind, message FROM journal ORDER BY id DESC LIMIT ?`, limit)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []JournalEntry
	for rows.Next() {
		var e JournalEntry
		if err := rows.Scan(&e.ID, &e.LifeID, &e.At, &e.Kind, &e.Message); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func (s *Store) CountJournalEntries(ctx context.Context) (int64, error) {
	var n int64
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM journal`).Scan(&n)
	return n, err
}

// ------------------- Inflight -------------------

func (s *Store) MarkInflight(ctx context.Context, lifeID int64, kind, payload string) (int64, error) {
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO inflight (life_id, started_at, kind, payload, status)
		 VALUES (?, ?, ?, ?, 'open')`,
		lifeID, time.Now().UTC(), kind, payload)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (s *Store) ClearInflight(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE inflight SET status = 'done' WHERE id = ?`, id)
	return err
}

func (s *Store) ListInflight(ctx context.Context) ([]InflightTask, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, life_id, started_at, kind, payload, status
		 FROM inflight WHERE status = 'open' ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []InflightTask
	for rows.Next() {
		var t InflightTask
		if err := rows.Scan(&t.ID, &t.LifeID, &t.StartedAt, &t.Kind, &t.Payload, &t.Status); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (s *Store) CountInflight(ctx context.Context) (int64, error) {
	var n int64
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM inflight WHERE status = 'open'`).Scan(&n)
	return n, err
}

// CloseStaleInflights marks all open inflight tasks from prior lives as done.
// Called on boot so that tasks left open by a crashed life don't linger.
func (s *Store) CloseStaleInflights(ctx context.Context, currentLifeID int64) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE inflight SET status = 'done' WHERE status = 'open' AND life_id != ?`,
		currentLifeID)
	return err
}

// ------------------- Sessions -------------------

func (s *Store) PutSession(ctx context.Context, purpose, sessionID string) error {
	now := time.Now().UTC()
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO sessions (purpose, session_id, created_at, last_used_at) VALUES (?, ?, ?, ?)
		 ON CONFLICT(purpose) DO UPDATE SET session_id = excluded.session_id, last_used_at = excluded.last_used_at`,
		purpose, sessionID, now, now)
	return err
}

func (s *Store) GetSession(ctx context.Context, purpose string) (string, bool, error) {
	var id string
	err := s.db.QueryRowContext(ctx,
		`SELECT session_id FROM sessions WHERE purpose = ?`, purpose).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return id, true, nil
}

// ------------------- Secrets -------------------

func (s *Store) PutSecret(ctx context.Context, key, value string) error {
	now := time.Now().UTC()
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO secrets (key, value, created_at, updated_at) VALUES (?, ?, ?, ?)
		 ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`,
		key, value, now, now)
	return err
}

func (s *Store) GetSecret(ctx context.Context, key string) (string, bool, error) {
	var v string
	err := s.db.QueryRowContext(ctx,
		`SELECT value FROM secrets WHERE key = ?`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return v, true, nil
}

func (s *Store) DeleteSecret(ctx context.Context, key string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM secrets WHERE key = ?`, key)
	return err
}

func (s *Store) ListSecretKeys(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT key FROM secrets ORDER BY key`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []string
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			return nil, err
		}
		out = append(out, k)
	}
	return out, rows.Err()
}

// ------------------- Messages -------------------

type Message struct {
	ID         int64
	ReceivedAt time.Time
	Content    string
}

// AddMessage queues a message from the user for Kenny to see on next boot.
// Returns the stored message (including its ID and received_at timestamp).
func (s *Store) AddMessage(ctx context.Context, content string) (Message, error) {
	at := time.Now().UTC()
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO messages (received_at, content) VALUES (?, ?)`, at, content)
	if err != nil {
		return Message{}, err
	}
	id, _ := res.LastInsertId()
	return Message{ID: id, ReceivedAt: at, Content: content}, nil
}

// PendingMessages returns messages not yet consumed by a boot prompt.
func (s *Store) PendingMessages(ctx context.Context) ([]Message, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, received_at, content FROM messages WHERE consumed_at IS NULL ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Message
	for rows.Next() {
		var m Message
		if err := rows.Scan(&m.ID, &m.ReceivedAt, &m.Content); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// ConsumeMessages marks all pending messages as consumed.
func (s *Store) ConsumeMessages(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE messages SET consumed_at = ? WHERE consumed_at IS NULL`,
		time.Now().UTC())
	return err
}
