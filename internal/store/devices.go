package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// SyncDevice 把配置清单中的一台设备 upsert 进 devices 表。
// 每次启动调用：YAML 是清单的 source of truth，DB 侧保留运行时状态字段。
func (s *Store) SyncDevice(ctx context.Context, name, host string, port int, dialect string) (int64, error) {
	now := nowTS()
	_, err := s.w.ExecContext(ctx, `
		INSERT INTO devices (name, host, port, dialect, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(name) DO UPDATE SET
			host = excluded.host,
			port = excluded.port,
			dialect = excluded.dialect,
			updated_at = excluded.updated_at`,
		name, host, port, dialect, now, now)
	if err != nil {
		return 0, fmt.Errorf("同步设备 %s: %w", name, err)
	}
	d, err := s.GetDeviceByName(ctx, name)
	if err != nil {
		return 0, err
	}
	return d.ID, nil
}

const deviceCols = `id, name, host, port, dialect, last_seen_at, last_error, created_at, updated_at`

func scanDevice(row interface{ Scan(...any) error }) (*Device, error) {
	var (
		d                    Device
		lastSeen, lastErr    sql.NullString
		createdAt, updatedAt string
	)
	if err := row.Scan(&d.ID, &d.Name, &d.Host, &d.Port, &d.Dialect,
		&lastSeen, &lastErr, &createdAt, &updatedAt); err != nil {
		return nil, err
	}
	d.LastError = lastErr.String
	var err error
	if d.LastSeenAt, err = timePtr(lastSeen); err != nil {
		return nil, err
	}
	if d.CreatedAt, err = ParseTime(createdAt); err != nil {
		return nil, err
	}
	if d.UpdatedAt, err = ParseTime(updatedAt); err != nil {
		return nil, err
	}
	return &d, nil
}

// GetDeviceByName 按名称取设备。
func (s *Store) GetDeviceByName(ctx context.Context, name string) (*Device, error) {
	row := s.r.QueryRowContext(ctx, "SELECT "+deviceCols+" FROM devices WHERE name = ?", name)
	d, err := scanDevice(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("查询设备 %s: %w", name, err)
	}
	return d, nil
}

// GetDeviceByID 按 ID 取设备。
func (s *Store) GetDeviceByID(ctx context.Context, id int64) (*Device, error) {
	row := s.r.QueryRowContext(ctx, "SELECT "+deviceCols+" FROM devices WHERE id = ?", id)
	d, err := scanDevice(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("查询设备 ID=%d: %w", id, err)
	}
	return d, nil
}

// ListDevices 返回全部设备（按名称排序）。
func (s *Store) ListDevices(ctx context.Context) ([]*Device, error) {
	rows, err := s.r.QueryContext(ctx, "SELECT "+deviceCols+" FROM devices ORDER BY name")
	if err != nil {
		return nil, fmt.Errorf("查询设备列表: %w", err)
	}
	defer rows.Close()
	out := []*Device{}
	for rows.Next() {
		d, err := scanDevice(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// MarkDeviceSeen 记录一次成功接触。
func (s *Store) MarkDeviceSeen(ctx context.Context, id int64) error {
	now := nowTS()
	_, err := s.w.ExecContext(ctx, `
		UPDATE devices SET last_seen_at = ?, last_error = NULL, updated_at = ? WHERE id = ?`,
		now, now, id)
	if err != nil {
		return fmt.Errorf("更新设备可见时间: %w", err)
	}
	return nil
}

// MarkDeviceError 记录一次接触失败。
func (s *Store) MarkDeviceError(ctx context.Context, id int64, msg string) error {
	now := nowTS()
	_, err := s.w.ExecContext(ctx, `
		UPDATE devices SET last_error = ?, updated_at = ? WHERE id = ?`,
		msg, now, id)
	if err != nil {
		return fmt.Errorf("更新设备错误: %w", err)
	}
	return nil
}
