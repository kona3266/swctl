// Package store 负责 SQLite 持久化：连接管理、迁移与各表 DAO。
package store

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite" // 注册 "sqlite" 驱动（纯 Go，无 CGO）
)

// ErrNotFound 表示查询目标不存在。
var ErrNotFound = errors.New("记录不存在")

// Store 持有两个连接池：写池单连接串行化写入，读池小并发。
type Store struct {
	w *sql.DB
	r *sql.DB
}

// Open 打开（必要时创建）数据库并执行迁移。
func Open(path string) (*Store, error) {
	if path == "" {
		return nil, errors.New("数据库路径为空")
	}
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, fmt.Errorf("创建数据目录: %w", err)
		}
	}
	dsn := buildDSN(path)
	w, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("打开写连接: %w", err)
	}
	// 写池收敛到单连接：避免 SQLITE_BUSY，写事务天然串行。
	w.SetMaxOpenConns(1)
	w.SetMaxIdleConns(1)
	w.SetConnMaxLifetime(0)
	if err := w.Ping(); err != nil {
		_ = w.Close()
		return nil, fmt.Errorf("连接数据库 %s: %w", path, err)
	}
	r, err := sql.Open("sqlite", dsn)
	if err != nil {
		_ = w.Close()
		return nil, fmt.Errorf("打开读连接: %w", err)
	}
	r.SetMaxOpenConns(4)
	r.SetMaxIdleConns(4)
	r.SetConnMaxLifetime(0)

	s := &Store{w: w, r: r}
	// 启动自检：断电/WAL 残留导致的库损坏要在启动时暴露，而不是等到写入失败。
	var check string
	if err := w.QueryRow("PRAGMA quick_check").Scan(&check); err != nil {
		_ = s.Close()
		return nil, fmt.Errorf("数据库自检失败: %w", err)
	}
	if check != "ok" {
		_ = s.Close()
		return nil, fmt.Errorf("数据库自检未通过（%s），请修复或重建 data_dir", check)
	}
	if err := s.migrate(); err != nil {
		_ = s.Close()
		return nil, err
	}
	return s, nil
}

// buildDSN 组装 modernc.org/sqlite 的 DSN。
// pragma 是连接级设置，必须写进 DSN，否则连接池新建的连接会丢失。
// path 以 "file:" 开头时视为完整 DSN（测试用内存库）。
func buildDSN(path string) string {
	const params = "_pragma=busy_timeout(5000)" +
		"&_pragma=journal_mode(WAL)" +
		"&_pragma=foreign_keys(1)" +
		"&_pragma=synchronous(NORMAL)" +
		"&_txlock=immediate"
	if strings.HasPrefix(path, "file:") {
		if strings.Contains(path, "?") {
			return path + "&" + params
		}
		return path + "?" + params
	}
	return "file:" + path + "?" + params
}

// Write 返回写连接池（单连接）。
func (s *Store) Write() *sql.DB { return s.w }

// Read 返回读连接池。
func (s *Store) Read() *sql.DB { return s.r }

// Close 关闭连接池。
func (s *Store) Close() error {
	var errs []error
	if s.w != nil {
		errs = append(errs, s.w.Close())
	}
	if s.r != nil {
		errs = append(errs, s.r.Close())
	}
	return errors.Join(errs...)
}

// --- 时间与工具 ---

// nowTime 返回当前 UTC 时间。
func nowTime() time.Time { return time.Now().UTC() }

// nowTS 返回存储用时间戳（RFC3339，UTC）。
func nowTS() string { return FormatTime(time.Now()) }

// FormatTime 把时间格式化为存储格式。
func FormatTime(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }

// ParseTime 解析存储格式时间；空串返回零值。
func ParseTime(s string) (time.Time, error) {
	if s == "" {
		return time.Time{}, nil
	}
	return time.Parse(time.RFC3339Nano, s)
}

// nullableTime 把 *time.Time 转成可写入的 any。
func nullableTime(t *time.Time) any {
	if t == nil {
		return nil
	}
	return FormatTime(*t)
}

// timePtr 解析可空时间列。
func timePtr(s sql.NullString) (*time.Time, error) {
	if !s.Valid || s.String == "" {
		return nil, nil
	}
	t, err := time.Parse(time.RFC3339Nano, s.String)
	if err != nil {
		return nil, err
	}
	return &t, nil
}
