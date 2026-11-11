package main

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite"
)

type Store struct {
	db *sql.DB
}

func openStore(dataDir string) (*Store, error) {
	if dataDir == "" {
		return nil, fmt.Errorf("必须指定数据目录")
	}
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return nil, fmt.Errorf("无法创建数据目录: %w", err)
	}
	if err := os.Chmod(dataDir, 0o700); err != nil {
		return nil, fmt.Errorf("无法保护数据目录: %w", err)
	}
	dbPath := filepath.Join(dataDir, "share.sqlite3")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("无法打开 SQLite 数据库: %w", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	db.SetConnMaxLifetime(0)
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("无法连接 SQLite 数据库: %w", err)
	}
	for _, pragma := range []string{
		"PRAGMA busy_timeout=5000",
		"PRAGMA journal_mode=WAL",
		"PRAGMA synchronous=FULL",
		"PRAGMA foreign_keys=ON",
	} {
		if _, err := db.Exec(pragma); err != nil {
			db.Close()
			return nil, fmt.Errorf("无法配置 SQLite: %w", err)
		}
	}
	if err := migrate(db); err != nil {
		db.Close()
		return nil, err
	}
	if err := os.Chmod(dbPath, 0o600); err != nil {
		db.Close()
		return nil, fmt.Errorf("无法保护 SQLite 数据库: %w", err)
	}
	return &Store{db: db}, nil
}

func (s *Store) close() error { return s.db.Close() }

func migrate(db *sql.DB) error {
	const schema = `
CREATE TABLE IF NOT EXISTS admin (
  singleton INTEGER PRIMARY KEY CHECK (singleton = 1),
  password_salt BLOB NOT NULL,
  password_hash BLOB NOT NULL,
  iterations INTEGER NOT NULL,
  created_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS setup_tokens (
  singleton INTEGER PRIMARY KEY CHECK (singleton = 1),
  token_hash TEXT NOT NULL,
  created_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS admin_sessions (
  token_hash TEXT PRIMARY KEY,
  expires_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS connections (
  id TEXT PRIMARY KEY,
  name TEXT NOT NULL,
  view_code TEXT NOT NULL UNIQUE,
  paired INTEGER NOT NULL DEFAULT 0 CHECK (paired IN (0, 1)),
  public_enabled INTEGER NOT NULL DEFAULT 0 CHECK (public_enabled IN (0, 1)),
  last_sequence INTEGER NOT NULL DEFAULT 0 CHECK (last_sequence >= 0),
  created_at TEXT NOT NULL,
  binding_hash TEXT UNIQUE,
  binding_expires_at TEXT
);
CREATE TABLE IF NOT EXISTS publishers (
  connection_id TEXT PRIMARY KEY REFERENCES connections(id) ON DELETE CASCADE,
  token_hash TEXT NOT NULL UNIQUE,
  created_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS snapshots (
  connection_id TEXT PRIMARY KEY REFERENCES connections(id) ON DELETE CASCADE,
  sequence INTEGER NOT NULL,
  generated_at TEXT NOT NULL,
  source_updated_at TEXT,
  received_at TEXT NOT NULL,
  json TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS admin_sessions_expiry ON admin_sessions(expires_at);
CREATE INDEX IF NOT EXISTS connections_binding_expiry ON connections(binding_expires_at);
`
	if _, err := db.Exec(schema); err != nil {
		return fmt.Errorf("无法初始化数据库结构: %w", err)
	}
	if err := ensureColumn(db, "connections", "last_sequence", `ALTER TABLE connections ADD COLUMN last_sequence INTEGER NOT NULL DEFAULT 0 CHECK (last_sequence >= 0)`); err != nil {
		return fmt.Errorf("无法升级数据库结构: %w", err)
	}
	_, err := db.Exec(`DELETE FROM admin_sessions WHERE expires_at <= strftime('%Y-%m-%dT%H:%M:%fZ','now')`)
	if err != nil {
		return fmt.Errorf("无法清理过期会话: %w", err)
	}
	return nil
}

func ensureColumn(db *sql.DB, table, column, alter string) error {
	rows, err := db.Query(`PRAGMA table_info(` + table + `)`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name, kind string
		var notNull, primary int
		var defaultValue any
		if err := rows.Scan(&cid, &name, &kind, &notNull, &defaultValue, &primary); err != nil {
			return err
		}
		if name == column {
			return rows.Err()
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	_, err = db.Exec(alter)
	return err
}

func (s *Store) hasAdmin(ctx context.Context) (bool, error) {
	var exists int
	err := s.db.QueryRowContext(ctx, `SELECT 1 FROM admin WHERE singleton=1`).Scan(&exists)
	if err == sql.ErrNoRows {
		return false, nil
	}
	return err == nil, err
}
