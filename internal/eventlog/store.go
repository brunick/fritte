package eventlog

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

// Store verwaltet die Persistierung von Eventlog-Eintraegen in PostgreSQL.
type Store struct {
	db *sql.DB
}

// NewStore oeffnet die Datenbankverbindung und stellt das Schema bereit.
func NewStore(dsn string) (*Store, error) {
	if dsn == "" {
		return nil, fmt.Errorf("DATABASE_URL fehlt")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, fmt.Errorf("postgres open: %w", err)
	}
	db.SetMaxOpenConns(5)
	db.SetMaxIdleConns(2)
	db.SetConnMaxLifetime(5 * time.Minute)

	store := &Store{db: db}
	if err := store.migrate(); err != nil {
		return nil, err
	}
	return store, nil
}

// Close schliesst die Datenbankverbindung.
func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

func (s *Store) migrate() error {
	const ddl = `
CREATE TABLE IF NOT EXISTS eventlog (
    id BIGSERIAL PRIMARY KEY,
    entry_hash TEXT NOT NULL UNIQUE,
    event_time TEXT NOT NULL,
    event_group TEXT NOT NULL,
    event_id INT NOT NULL,
    msg TEXT NOT NULL,
    event_date TEXT NOT NULL,
    nohelp BOOLEAN NOT NULL,
    scraped_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    sent_to_syslog BOOLEAN NOT NULL DEFAULT FALSE
);
CREATE INDEX IF NOT EXISTS idx_eventlog_unsent ON eventlog(sent_to_syslog) WHERE sent_to_syslog = FALSE;
`
	if _, err := s.db.Exec(ddl); err != nil {
		return fmt.Errorf("postgres migrate: %w", err)
	}
	return nil
}

// SaveEntries fuegt neue Eventlog-Eintraege ein. Bereits bekannte Eintraege
// werden uebersprungen (UNIQUE entry_hash).
func (s *Store) SaveEntries(ctx context.Context, entries []Entry) error {
	if len(entries) == 0 {
		return nil
	}
	const q = `
		INSERT INTO eventlog (entry_hash, event_time, event_group, event_id, msg, event_date, nohelp, scraped_at, sent_to_syslog)
		VALUES ($1, $2, $3, $4, $5, $6, $7, NOW(), FALSE)
		ON CONFLICT (entry_hash) DO NOTHING
	`
	for _, e := range entries {
		if _, err := s.db.ExecContext(ctx, q, e.Hash(), e.Time, e.Group, e.ID, e.Msg, e.Date, e.NoHelp); err != nil {
			return fmt.Errorf("eventlog insert: %w", err)
		}
	}
	return nil
}

// UnsentEntries liefert alle noch nicht gesendeten Eintraege.
func (s *Store) UnsentEntries(ctx context.Context) ([]EntryWithID, error) {
	const q = `
		SELECT id, event_time, event_group, event_id, msg, event_date, nohelp
		FROM eventlog
		WHERE sent_to_syslog = FALSE
		ORDER BY id ASC
	`
	rows, err := s.db.QueryContext(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("eventlog unsent query: %w", err)
	}
	defer rows.Close()

	var out []EntryWithID
	for rows.Next() {
		var r EntryWithID
		if err := rows.Scan(&r.DBID, &r.Entry.Time, &r.Entry.Group, &r.Entry.ID,
			&r.Entry.Msg, &r.Entry.Date, &r.Entry.NoHelp); err != nil {
			return nil, fmt.Errorf("eventlog scan: %w", err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("eventlog rows: %w", err)
	}
	return out, nil
}

// MarkSent markiert Eintraege als an Syslog gesendet.
func (s *Store) MarkSent(ctx context.Context, ids []int64) error {
	if len(ids) == 0 {
		return nil
	}
	const q = `UPDATE eventlog SET sent_to_syslog = TRUE WHERE id = $1`
	for _, id := range ids {
		if _, err := s.db.ExecContext(ctx, q, id); err != nil {
			return fmt.Errorf("eventlog mark sent %d: %w", id, err)
		}
	}
	return nil
}

// EntryWithID koppelt einen Eintrag mit seiner Datenbank-ID.
type EntryWithID struct {
	DBID  int64
	Entry Entry
}
