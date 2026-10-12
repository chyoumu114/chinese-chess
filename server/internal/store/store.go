// Package store owns the MySQL connection, the schema migration and every
// query the API needs.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/go-sql-driver/mysql"

	"xiangqi/internal/config"
)

// ErrNotFound is returned when a lookup matches no row.
var ErrNotFound = errors.New("store: not found")

// ErrDuplicate is returned when a unique constraint is violated.
var ErrDuplicate = errors.New("store: duplicate entry")

// Store is the database handle.
type Store struct {
	db *sql.DB
}

// Open connects to MySQL. When create is true and the schema does not exist it
// is created first, which keeps local setup to a single command.
func Open(ctx context.Context, cfg config.DBConfig, create bool) (*Store, error) {
	if create {
		if err := ensureDatabase(ctx, cfg); err != nil {
			return nil, err
		}
	}

	db, err := sql.Open("mysql", cfg.DSN())
	if err != nil {
		return nil, fmt.Errorf("store: open: %w", err)
	}
	db.SetMaxOpenConns(cfg.MaxOpenConns)
	db.SetMaxIdleConns(cfg.MaxIdleConns)
	db.SetConnMaxLifetime(cfg.ConnMaxLifetime)

	pingCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := db.PingContext(pingCtx); err != nil {
		db.Close()
		return nil, fmt.Errorf("store: ping %s:%d/%s: %w", cfg.Host, cfg.Port, cfg.Name, err)
	}
	return &Store{db: db}, nil
}

// ensureDatabase creates the schema when it is missing.
func ensureDatabase(ctx context.Context, cfg config.DBConfig) error {
	db, err := sql.Open("mysql", cfg.DSNWithoutDatabase())
	if err != nil {
		return fmt.Errorf("store: open server: %w", err)
	}
	defer db.Close()

	pingCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := db.PingContext(pingCtx); err != nil {
		return fmt.Errorf("store: ping server %s:%d: %w", cfg.Host, cfg.Port, err)
	}

	// The schema name cannot be parameterised, so it is validated instead: it is
	// used with backticks and must contain only identifier characters.
	if !isIdentifier(cfg.Name) {
		return fmt.Errorf("store: database name %q is not a plain identifier", cfg.Name)
	}
	stmt := fmt.Sprintf(
		"CREATE DATABASE IF NOT EXISTS `%s` CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci",
		cfg.Name)
	if _, err := db.ExecContext(ctx, stmt); err != nil {
		return fmt.Errorf("store: create database %s: %w", cfg.Name, err)
	}
	return nil
}

// isIdentifier reports whether s is safe to embed in a backticked identifier.
func isIdentifier(s string) bool {
	if s == "" || len(s) > 64 {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_':
		default:
			return false
		}
	}
	return true
}

// Close releases the connection pool.
func (s *Store) Close() error { return s.db.Close() }

// Ping reports whether the server is reachable, which the health endpoint uses.
func (s *Store) Ping(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	return s.db.PingContext(ctx)
}

// DB exposes the handle for health checks and tests.
func (s *Store) DB() *sql.DB { return s.db }

// Migrate applies the schema. It is idempotent and safe to run on every start.
func (s *Store) Migrate(ctx context.Context) error {
	for _, stmt := range schema {
		if _, err := s.db.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("store: migrate: %w\nstatement: %s", err, firstLine(stmt))
		}
	}
	return nil
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// wrap converts a driver error into one of the package sentinels.
func wrap(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	var me *mysql.MySQLError
	if errors.As(err, &me) && me.Number == 1062 {
		return fmt.Errorf("%w: %s", ErrDuplicate, me.Message)
	}
	return err
}
