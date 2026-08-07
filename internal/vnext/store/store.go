package store

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

// Store owns the VNext database. It intentionally has no dependency on the
// legacy store package so a fresh database cannot fall back to legacy state.
type Store struct {
	DB *sql.DB
}

type Options struct {
	MaxOpenConns int
	MaxIdleConns int
}

func Open(ctx context.Context, path string) (*Store, error) {
	return OpenWithOptions(ctx, path, Options{})
}

func OpenWithOptions(ctx context.Context, path string, options Options) (*Store, error) {
	if options.MaxOpenConns == 0 {
		options.MaxOpenConns = 4
		options.MaxIdleConns = 2
	}
	if options.MaxOpenConns < 1 || options.MaxOpenConns > 32 {
		return nil, fmt.Errorf("database maximum open connections must be between 1 and 32")
	}
	if options.MaxIdleConns < 0 || options.MaxIdleConns > options.MaxOpenConns {
		return nil, fmt.Errorf("database maximum idle connections must be between 0 and maximum open connections")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("create database directory: %w", err)
	}
	dsn := "file:" + filepath.ToSlash(path) +
		"?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)" +
		"&_pragma=synchronous(NORMAL)&_pragma=wal_autocheckpoint(1000)&_pragma=journal_size_limit(67108864)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(options.MaxOpenConns)
	db.SetMaxIdleConns(options.MaxIdleConns)
	db.SetConnMaxLifetime(0)
	db.SetConnMaxIdleTime(5 * time.Minute)
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	result := &Store{DB: db}
	if err := result.Migrate(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	settings, err := result.GetRuntimeSettings(ctx)
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := result.synchronizeModelMonitorIntervals(ctx, settings.ProbeInterval, time.Now().UTC()); err != nil {
		_ = db.Close()
		return nil, err
	}
	return result, nil
}

// Maintain performs bounded, non-blocking SQLite housekeeping after history
// pruning. PASSIVE checkpointing never waits for active readers, which keeps
// request forwarding independent from maintenance on small hosts.
func (s *Store) Maintain(ctx context.Context) error {
	if s == nil || s.DB == nil {
		return fmt.Errorf("store is unavailable")
	}
	if _, err := s.DB.ExecContext(ctx, `PRAGMA optimize`); err != nil {
		return fmt.Errorf("optimize SQLite planner state: %w", err)
	}
	var busy, logFrames, checkpointedFrames int
	if err := s.DB.QueryRowContext(ctx, `PRAGMA wal_checkpoint(PASSIVE)`).Scan(
		&busy, &logFrames, &checkpointedFrames,
	); err != nil {
		return fmt.Errorf("checkpoint SQLite WAL: %w", err)
	}
	return nil
}

func (s *Store) Close() error {
	return s.DB.Close()
}

func NowMS() int64 {
	return time.Now().UTC().UnixMilli()
}

type scanner interface {
	Scan(...any) error
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func nullableString(value string) any {
	if value == "" {
		return nil
	}
	return value
}
