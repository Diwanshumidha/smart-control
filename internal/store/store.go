// Package store owns the SQLite database and its schema migrations. Domain
// packages (posts, resources, activity, urlpolicy) keep their own queries and
// receive the *sql.DB from here.
package store

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"sort"
	"time"

	_ "modernc.org/sqlite"
)

// Migration is one forward-only schema step. Packages add theirs with Register
// from an init func so new features don't have to touch this file.
type Migration struct {
	Version int
	Name    string
	SQL     string
}

var migrations []Migration

func Register(m Migration) { migrations = append(migrations, m) }

type Store struct{ DB *sql.DB }

func Open(dataDir string) (*Store, error) {
	dsn := "file:" + filepath.Join(dataDir, "smart-control.db") +
		"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(4)
	s := &Store{DB: db}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() error { return s.DB.Close() }

func (s *Store) migrate() error {
	if _, err := s.DB.Exec(`CREATE TABLE IF NOT EXISTS schema_migrations (version INTEGER PRIMARY KEY, name TEXT, applied_at INTEGER)`); err != nil {
		return err
	}
	sort.Slice(migrations, func(i, j int) bool { return migrations[i].Version < migrations[j].Version })
	seen := map[int]string{}
	for _, m := range migrations {
		if prev, dup := seen[m.Version]; dup {
			return fmt.Errorf("migration version %d used by both %q and %q", m.Version, prev, m.Name)
		}
		seen[m.Version] = m.Name
		var n int
		if err := s.DB.QueryRow(`SELECT COUNT(*) FROM schema_migrations WHERE version = ?`, m.Version).Scan(&n); err != nil {
			return err
		}
		if n > 0 {
			continue
		}
		tx, err := s.DB.Begin()
		if err != nil {
			return err
		}
		if _, err := tx.Exec(m.SQL); err != nil {
			tx.Rollback()
			return fmt.Errorf("migration %d (%s): %w", m.Version, m.Name, err)
		}
		if _, err := tx.Exec(`INSERT INTO schema_migrations(version, name, applied_at) VALUES (?, ?, ?)`, m.Version, m.Name, Now()); err != nil {
			tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}

// Now is the timestamp format used in every table: unix milliseconds.
func Now() int64 { return time.Now().UnixMilli() }
