package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
)

// TestMigrateLegacyV2Schema 模拟一个已应用 001（旧形态）且带数据的库，
// 验证 002 能原地清理字段并保留数据。
func TestMigrateLegacyV2Schema(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "legacy.db")

	db, err := sql.Open("sqlite", buildDSN(path))
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`
		CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY, name TEXT NOT NULL, applied_at TEXT NOT NULL);
		INSERT INTO schema_migrations VALUES (1, '001_init.sql', '2026-01-01T00:00:00Z');

		CREATE TABLE devices (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			name TEXT NOT NULL UNIQUE,
			host TEXT NOT NULL,
			port INTEGER NOT NULL DEFAULT 22,
			dialect TEXT NOT NULL,
			enabled INTEGER NOT NULL DEFAULT 1,
			capabilities TEXT,
			last_seen_at TEXT,
			last_error TEXT,
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL
		);
		CREATE TABLE device_configs (
			device_id INTEGER PRIMARY KEY REFERENCES devices(id),
			model_json TEXT NOT NULL,
			version INTEGER NOT NULL DEFAULT 0,
			sync_state TEXT NOT NULL,
			updated_at TEXT NOT NULL,
			last_reconciled_at TEXT
		);
		CREATE TABLE operations (
			id TEXT PRIMARY KEY,
			device_id INTEGER NOT NULL REFERENCES devices(id),
			kind TEXT NOT NULL,
			status TEXT NOT NULL,
			created_at TEXT NOT NULL
		);
		INSERT INTO devices (name, host, port, dialect, enabled, capabilities, created_at, updated_at)
		VALUES ('sw1', '10.0.0.1', 22, 'cisco_ios', 1, '{"version":"x"}', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z');
		INSERT INTO device_configs (device_id, model_json, version, sync_state, updated_at)
		VALUES (1, '{}', 0, 'unknown', '2026-01-01T00:00:00Z');
		INSERT INTO operations (id, device_id, kind, status, created_at)
		VALUES ('op1', 1, 'apply', 'succeeded', '2026-01-01T00:00:00Z');
	`)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	st, err := Open(path)
	if err != nil {
		t.Fatalf("Open legacy db: %v", err)
	}
	defer func() { _ = st.Close() }()

	dev, err := st.GetDeviceByName(ctx, "sw1")
	if err != nil || dev.ID != 1 {
		t.Fatalf("设备数据未保留: %+v, %v", dev, err)
	}

	var enabledCols int
	if err := st.r.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM pragma_table_info('devices') WHERE name IN ('enabled','capabilities')").Scan(&enabledCols); err != nil {
		t.Fatal(err)
	}
	if enabledCols != 0 {
		t.Fatalf("遗留列仍存在: %d", enabledCols)
	}
}
