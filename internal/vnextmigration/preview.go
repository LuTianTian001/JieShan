package vnextmigration

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

type Options struct {
	NowMS int64
}

func PreviewSQLiteFile(ctx context.Context, path string, options Options) (Report, error) {
	absPath, err := filepath.Abs(path)
	if err != nil {
		return Report{}, fmt.Errorf("resolve source database path: %w", err)
	}
	info, err := os.Stat(absPath)
	if err != nil {
		return Report{}, fmt.Errorf("stat source database: %w", err)
	}
	if info.IsDir() {
		return Report{}, errors.New("source database path is a directory")
	}

	slashPath := filepath.ToSlash(absPath)
	if filepath.VolumeName(absPath) != "" && !strings.HasPrefix(slashPath, "/") {
		slashPath = "/" + slashPath
	}
	dsnURL := url.URL{Scheme: "file", Path: slashPath}
	dsn := dsnURL.String() + "?mode=ro&_pragma=query_only(1)&_pragma=busy_timeout(5000)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return Report{}, fmt.Errorf("open source database read-only: %w", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	defer db.Close()
	if err := db.PingContext(ctx); err != nil {
		return Report{}, fmt.Errorf("ping source database read-only: %w", err)
	}
	report, err := PreviewDatabase(ctx, db, options)
	if err != nil {
		return Report{}, err
	}
	report.Source.Path = absPath
	return report, nil
}

func PreviewDatabase(ctx context.Context, db *sql.DB, options Options) (Report, error) {
	if db == nil {
		return Report{}, errors.New("source database is nil")
	}
	nowMS := options.NowMS
	if nowMS <= 0 {
		nowMS = time.Now().UTC().UnixMilli()
	}
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return Report{}, fmt.Errorf("begin source snapshot: %w", err)
	}
	defer tx.Rollback()

	schema, err := inspectSchema(ctx, tx)
	if err != nil {
		return Report{}, err
	}
	if !schema.hasTable("downstream_keys") {
		return Report{}, errors.New("source database does not contain downstream_keys")
	}
	inventory, err := loadInventory(ctx, tx, schema)
	if err != nil {
		return Report{}, err
	}
	report := materializeReport(inventory, schema, nowMS)
	if err := tx.Commit(); err != nil {
		return Report{}, fmt.Errorf("finish source snapshot: %w", err)
	}
	return report, nil
}

type schemaInfo struct {
	tables  map[string]bool
	columns map[string]map[string]bool
	version int
}

func (s schemaInfo) hasTable(name string) bool {
	return s.tables[name]
}

func (s schemaInfo) hasColumn(table, name string) bool {
	return s.columns[table][name]
}

func inspectSchema(ctx context.Context, tx *sql.Tx) (schemaInfo, error) {
	schema := schemaInfo{tables: map[string]bool{}, columns: map[string]map[string]bool{}}
	rows, err := tx.QueryContext(ctx, `SELECT name FROM sqlite_master WHERE type='table' ORDER BY name`)
	if err != nil {
		return schema, fmt.Errorf("list source tables: %w", err)
	}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			rows.Close()
			return schema, fmt.Errorf("scan source table: %w", err)
		}
		schema.tables[name] = true
	}
	if err := rows.Close(); err != nil {
		return schema, fmt.Errorf("close source table list: %w", err)
	}

	tables := make([]string, 0, len(schema.tables))
	for table := range schema.tables {
		tables = append(tables, table)
	}
	sort.Strings(tables)
	for _, table := range tables {
		columnRows, err := tx.QueryContext(ctx, `SELECT name FROM pragma_table_info(?)`, table)
		if err != nil {
			return schema, fmt.Errorf("inspect %s columns: %w", table, err)
		}
		schema.columns[table] = map[string]bool{}
		for columnRows.Next() {
			var name string
			if err := columnRows.Scan(&name); err != nil {
				columnRows.Close()
				return schema, fmt.Errorf("scan %s column: %w", table, err)
			}
			schema.columns[table][name] = true
		}
		if err := columnRows.Close(); err != nil {
			return schema, fmt.Errorf("close %s column list: %w", table, err)
		}
	}
	if schema.hasTable("schema_migrations") {
		if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(version),0) FROM schema_migrations`).Scan(&schema.version); err != nil {
			return schema, fmt.Errorf("read source schema version: %w", err)
		}
	}
	return schema, nil
}
