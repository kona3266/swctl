package store

import (
	"embed"
	"fmt"
	"io/fs"
	"sort"
	"strconv"
	"strings"
)

//go:embed migrations/*.sql
var migrationFS embed.FS

// migrate 在写连接上串行应用未执行的迁移。迁移文件命名：<版本号>_<名称>.sql。
func (s *Store) migrate() error {
	if _, err := s.w.Exec(`CREATE TABLE IF NOT EXISTS schema_migrations (
		version    INTEGER PRIMARY KEY,
		name       TEXT    NOT NULL,
		applied_at TEXT    NOT NULL)`); err != nil {
		return fmt.Errorf("创建 schema_migrations: %w", err)
	}

	applied := map[int]bool{}
	maxApplied := 0
	rows, err := s.w.Query("SELECT version FROM schema_migrations")
	if err != nil {
		return fmt.Errorf("查询已应用迁移: %w", err)
	}
	for rows.Next() {
		var v int
		if err := rows.Scan(&v); err != nil {
			rows.Close()
			return err
		}
		applied[v] = true
		if v > maxApplied {
			maxApplied = v
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	type mig struct {
		version int
		name    string
		sql     string
	}
	var pending []mig
	entries, err := fs.ReadDir(migrationFS, "migrations")
	if err != nil {
		return fmt.Errorf("读取迁移目录: %w", err)
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		version, err := parseMigrationVersion(e.Name())
		if err != nil {
			return err
		}
		if applied[version] {
			continue
		}
		raw, err := migrationFS.ReadFile("migrations/" + e.Name())
		if err != nil {
			return err
		}
		pending = append(pending, mig{version: version, name: e.Name(), sql: string(raw)})
	}
	sort.Slice(pending, func(i, j int) bool { return pending[i].version < pending[j].version })

	// 防降级：库里已应用的 schema 版本高于本次二进制支持的最大版本时，
	// 说明是用更旧的程序启动更新的库，继续运行会写坏数据 → 直接拒绝。
	if len(pending) > 0 && maxApplied > pending[len(pending)-1].version {
		return fmt.Errorf("数据库 schema（v%d）高于本程序支持的最高版本（v%d），拒绝降级启动",
			maxApplied, pending[len(pending)-1].version)
	}

	for _, m := range pending {
		tx, err := s.w.Begin()
		if err != nil {
			return fmt.Errorf("迁移 %s 开启事务: %w", m.name, err)
		}
		// modernc.org/sqlite 的 Exec 支持一次执行多条语句。
		if _, err := tx.Exec(m.sql); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("迁移 %s 执行失败: %w", m.name, err)
		}
		if _, err := tx.Exec(
			"INSERT INTO schema_migrations (version, name, applied_at) VALUES (?, ?, ?)",
			m.version, m.name, nowTS()); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("迁移 %s 记录失败: %w", m.name, err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("迁移 %s 提交失败: %w", m.name, err)
		}
	}
	return nil
}

// parseMigrationVersion 从文件名前缀解析版本号，如 001_init.sql → 1。
func parseMigrationVersion(name string) (int, error) {
	idx := strings.IndexByte(name, '_')
	if idx <= 0 {
		return 0, fmt.Errorf("迁移文件名 %q 缺少 <版本号>_ 前缀", name)
	}
	v, err := strconv.Atoi(name[:idx])
	if err != nil {
		return 0, fmt.Errorf("迁移文件名 %q 版本号非法: %w", name, err)
	}
	return v, nil
}
