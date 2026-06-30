// Package store persistiert Scraper-Snapshots pro FRITZ!Box-Modul in eigenen
// PostgreSQL-Tabellen. Fuer jedes Modul wird automatisch eine Tabelle
// module_<name> angelegt, sofern sie noch nicht existiert.
package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

// Store verwaltet die Verbindung zu PostgreSQL und das dynamische Schema fuer
// Modul-Snapshots.
type Store struct {
	db *sql.DB
}

// NewStore oeffnet die Datenbankverbindung und stellt sicher, dass das
// Verbindungspooling sinnvoll begrenzt ist.
func NewStore(dsn string) (*Store, error) {
	if dsn == "" {
		return nil, fmt.Errorf("DATABASE_URL fehlt")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, fmt.Errorf("postgres open: %w", err)
	}
	db.SetMaxOpenConns(10)
	db.SetMaxIdleConns(3)
	db.SetConnMaxLifetime(5 * time.Minute)

	return &Store{db: db}, nil
}

// Close schliesst die Datenbankverbindung.
func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

// Save speichert einen Snapshot fuer ein Modul. Dabei wird die passende
// Tabelle automatisch angelegt, sofern noetig. Wenn sich gegenueber dem
// zuletzt gespeicherten Eintrag nichts geaendert hat, wird kein neuer Eintrag
// geschrieben, um die Datenbank nicht zu fuellen.
func (s *Store) Save(ctx context.Context, module string, ok bool, data []byte) error {
	if err := validModuleName(module); err != nil {
		return err
	}

	table := tableName(module)
	if err := s.ensureTable(ctx, table); err != nil {
		return err
	}

	changed, err := s.hasChanged(ctx, table, ok, data)
	if err != nil {
		return err
	}
	if !changed {
		return nil
	}

	const q = `INSERT INTO %s (scraped_at, ok, data) VALUES (NOW(), $1, $2)`
	if _, err := s.db.ExecContext(ctx, fmt.Sprintf(q, table), ok, data); err != nil {
		return fmt.Errorf("snapshot insert %s: %w", module, err)
	}
	return nil
}

// hasChanged vergleicht den neuen Snapshot mit dem juengsten Eintrag in der
// Tabelle. Dabei werden nur ok-Flag und JSON-Inhalt betrachtet.
func (s *Store) hasChanged(ctx context.Context, table string, ok bool, data []byte) (bool, error) {
	const q = `SELECT ok, data FROM %s ORDER BY scraped_at DESC LIMIT 1`
	var lastOk bool
	var lastData []byte
	if err := s.db.QueryRowContext(ctx, fmt.Sprintf(q, table)).Scan(&lastOk, &lastData); err != nil {
		if err == sql.ErrNoRows {
			return true, nil
		}
		return false, fmt.Errorf("snapshot compare %s: %w", table, err)
	}
	if lastOk != ok {
		return true, nil
	}
	if len(lastData) != len(data) {
		return true, nil
	}
	for i := range lastData {
		if lastData[i] != data[i] {
			return true, nil
		}
	}
	return false, nil
}

// ensureTable legt die Modul-Tabelle und den Index an, falls sie noch nicht
// existieren.
func (s *Store) ensureTable(ctx context.Context, table string) error {
	ddl := fmt.Sprintf(`
		CREATE TABLE IF NOT EXISTS %s (
			id BIGSERIAL PRIMARY KEY,
			scraped_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			ok BOOLEAN NOT NULL,
			data JSONB NOT NULL
		);
		CREATE INDEX IF NOT EXISTS idx_%s_scraped_at ON %s(scraped_at DESC);
	`, table, table, table)
	if _, err := s.db.ExecContext(ctx, ddl); err != nil {
		return fmt.Errorf("snapshot migrate %s: %w", table, err)
	}
	return nil
}

// tableName bildet einen gueltigen PostgreSQL-Tabellennamen aus dem
// Modulnamen. Da validModuleName nur erlaubte Zeichen zulaesst, muss der
// Name nicht extra gequotet werden.
func tableName(module string) string {
	return "module_" + module
}

// validModuleName erlaubt nur Module-Namen aus Buchstaben, Ziffern und
// Unterstrichen, die nicht mit einer Ziffer beginnen, um SQL-Injection ueber
// den Tabellennamen auszuschliessen.
func validModuleName(module string) error {
	if module == "" {
		return fmt.Errorf("snapshot module: name leer")
	}
	first := module[0]
	if !((first >= 'a' && first <= 'z') || (first >= 'A' && first <= 'Z') || first == '_') {
		return fmt.Errorf("snapshot module: name muss mit Buchstabe oder _ beginnen: %q", module)
	}
	for i := 0; i < len(module); i++ {
		c := module[i]
		if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '_') {
			return fmt.Errorf("snapshot module: ungueltiger name %q", module)
		}
	}
	return nil
}
