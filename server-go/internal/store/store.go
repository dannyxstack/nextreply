// Package store 打开 SQLite 数据库并执行迁移。
//
// 选 SQLite 是为了部署简单：单个文件、一个容器、挂一个卷即可。
// WAL 模式下读可以并发；写事务用 BEGIN IMMEDIATE 串行，配合 busy_timeout 避免 SQLITE_BUSY。
package store

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

//go:embed migrations/*.sql
var migrationFS embed.FS

func Open(path string) (*sql.DB, error) {
	if dir := filepath.Dir(path); dir != "" {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, fmt.Errorf("create data dir: %w", err)
		}
	}
	q := url.Values{}
	q.Add("_pragma", "journal_mode(WAL)")
	q.Add("_pragma", "busy_timeout(10000)")
	q.Add("_pragma", "foreign_keys(1)")
	q.Add("_pragma", "synchronous(NORMAL)")
	q.Set("_txlock", "immediate")
	db, err := sql.Open("sqlite", "file:"+path+"?"+q.Encode())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(8)
	db.SetConnMaxIdleTime(5 * time.Minute)
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

// Migrate 按文件名顺序执行尚未执行过的迁移。
func Migrate(ctx context.Context, db *sql.DB) error {
	if _, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (version TEXT PRIMARY KEY, applied_at INTEGER NOT NULL)`); err != nil {
		return err
	}
	entries, err := migrationFS.ReadDir("migrations")
	if err != nil {
		return err
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".sql") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)

	for _, name := range names {
		var exists int
		if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM schema_migrations WHERE version = ?`, name).Scan(&exists); err != nil {
			return err
		}
		if exists > 0 {
			continue
		}
		body, err := migrationFS.ReadFile("migrations/" + name)
		if err != nil {
			return err
		}
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, string(body)); err != nil {
			tx.Rollback()
			return fmt.Errorf("migration %s: %w", name, err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO schema_migrations (version, applied_at) VALUES (?, ?)`, name, time.Now().UnixMilli()); err != nil {
			tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}

// Backup 生成一份一致的数据库快照（服务运行中也可以执行）。
func Backup(ctx context.Context, db *sql.DB, dest string) error {
	if _, err := os.Stat(dest); err == nil {
		return fmt.Errorf("%s already exists", dest)
	}
	_, err := db.ExecContext(ctx, `VACUUM INTO ?`, dest)
	return err
}

// Cleanup 删除过期的一次性数据（验证码、授权码、票据、IP 计数、过期很久的 refresh token）。
func Cleanup(ctx context.Context, db *sql.DB, now time.Time) error {
	ms := now.UnixMilli()
	day := int64(24 * time.Hour / time.Millisecond)
	stmts := []struct {
		sql string
		arg int64
	}{
		{`DELETE FROM otp_codes WHERE expires_at < ?`, ms - day},
		{`DELETE FROM auth_codes WHERE expires_at < ?`, ms - day},
		{`DELETE FROM web_tickets WHERE expires_at < ?`, ms - day},
		{`DELETE FROM ip_counters WHERE expires_at < ?`, ms},
		{`DELETE FROM refresh_tokens WHERE expires_at < ?`, ms - 30*day},
	}
	for _, s := range stmts {
		if _, err := db.ExecContext(ctx, s.sql, s.arg); err != nil {
			return err
		}
	}
	return nil
}
