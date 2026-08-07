package store

import (
	"context"
	"path/filepath"
	"testing"
)

func TestOpenWithOptionsUsesBoundedSQLitePool(t *testing.T) {
	storage, err := OpenWithOptions(context.Background(), filepath.Join(t.TempDir(), "pool.sqlite"), Options{
		MaxOpenConns: 3,
		MaxIdleConns: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = storage.Close() })

	stats := storage.DB.Stats()
	if stats.MaxOpenConnections != 3 {
		t.Fatalf("MaxOpenConnections = %d, want 3", stats.MaxOpenConnections)
	}
	if err := storage.Maintain(context.Background()); err != nil {
		t.Fatalf("Maintain() error = %v", err)
	}
}

func TestOpenWithOptionsRejectsInvalidPool(t *testing.T) {
	path := filepath.Join(t.TempDir(), "invalid.sqlite")
	if _, err := OpenWithOptions(context.Background(), path, Options{MaxOpenConns: 33}); err == nil {
		t.Fatal("oversized SQLite pool was accepted")
	}
	if _, err := OpenWithOptions(context.Background(), path, Options{MaxOpenConns: 2, MaxIdleConns: 3}); err == nil {
		t.Fatal("idle SQLite pool larger than open pool was accepted")
	}
}

func TestSQLiteWALPolicyPersistsDataAcrossMaintenanceAndReopen(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "persistent.sqlite")
	storage, err := OpenWithOptions(ctx, path, Options{MaxOpenConns: 4, MaxIdleConns: 2})
	if err != nil {
		t.Fatal(err)
	}

	var journalMode string
	var foreignKeys, busyTimeout, synchronous, autoCheckpoint int
	var journalLimit int64
	checks := []struct {
		query string
		dest  any
	}{
		{`PRAGMA journal_mode`, &journalMode},
		{`PRAGMA foreign_keys`, &foreignKeys},
		{`PRAGMA busy_timeout`, &busyTimeout},
		{`PRAGMA synchronous`, &synchronous},
		{`PRAGMA wal_autocheckpoint`, &autoCheckpoint},
		{`PRAGMA journal_size_limit`, &journalLimit},
	}
	for _, check := range checks {
		if err := storage.DB.QueryRowContext(ctx, check.query).Scan(check.dest); err != nil {
			_ = storage.Close()
			t.Fatalf("%s: %v", check.query, err)
		}
	}
	if journalMode != "wal" || foreignKeys != 1 || busyTimeout != 5000 || synchronous != 1 ||
		autoCheckpoint != 1000 || journalLimit != 64<<20 {
		_ = storage.Close()
		t.Fatalf("SQLite policy = journal %q fk %d busy %d sync %d checkpoint %d limit %d",
			journalMode, foreignKeys, busyTimeout, synchronous, autoCheckpoint, journalLimit)
	}
	if _, err := storage.DB.ExecContext(ctx, `CREATE TABLE persistence_check(value TEXT NOT NULL)`); err != nil {
		_ = storage.Close()
		t.Fatal(err)
	}
	if _, err := storage.DB.ExecContext(ctx, `INSERT INTO persistence_check(value) VALUES ('kept')`); err != nil {
		_ = storage.Close()
		t.Fatal(err)
	}
	if err := storage.Maintain(ctx); err != nil {
		_ = storage.Close()
		t.Fatal(err)
	}
	if err := storage.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	var value, integrity string
	if err := reopened.DB.QueryRowContext(ctx, `SELECT value FROM persistence_check`).Scan(&value); err != nil {
		t.Fatal(err)
	}
	if err := reopened.DB.QueryRowContext(ctx, `PRAGMA integrity_check`).Scan(&integrity); err != nil {
		t.Fatal(err)
	}
	if value != "kept" || integrity != "ok" {
		t.Fatalf("reopened SQLite = value %q integrity %q", value, integrity)
	}
}
