package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

const configCols = `device_id, model_json, version, sync_state, updated_at, last_reconciled_at,
	verified_version, verified_at`

func scanConfig(row interface{ Scan(...any) error }) (*ConfigEntry, error) {
	var (
		ce             ConfigEntry
		updatedAt      string
		lastReconciled sql.NullString
		verifiedAt     sql.NullString
	)
	if err := row.Scan(&ce.DeviceID, &ce.ModelJSON, &ce.Version, &ce.SyncState, &updatedAt,
		&lastReconciled, &ce.VerifiedVersion, &verifiedAt); err != nil {
		return nil, err
	}
	var err error
	if ce.VerifiedAt, err = timePtr(verifiedAt); err != nil {
		return nil, err
	}
	if ce.UpdatedAt, err = ParseTime(updatedAt); err != nil {
		return nil, err
	}
	if ce.LastReconciledAt, err = timePtr(lastReconciled); err != nil {
		return nil, err
	}
	return &ce, nil
}

// EnsureConfig 设备没有模型行时创建空模型（新交换机默认空，不导入设备配置）。
func (s *Store) EnsureConfig(ctx context.Context, deviceID int64, emptyModelJSON string) error {
	now := nowTS()
	_, err := s.w.ExecContext(ctx, `
		INSERT INTO device_configs (device_id, model_json, version, sync_state, updated_at)
		VALUES (?, ?, 0, ?, ?)
		ON CONFLICT(device_id) DO NOTHING`,
		deviceID, emptyModelJSON, SyncUnknown, now)
	if err != nil {
		return fmt.Errorf("初始化设备配置模型: %w", err)
	}
	// 版本 0 也写入历史，保证首个变更失败回滚时能取到基线模型。
	if _, err := s.w.ExecContext(ctx, `
		INSERT INTO device_config_versions (device_id, version, model_json, created_at)
		VALUES (?, 0, ?, ?)
		ON CONFLICT(device_id, version) DO NOTHING`,
		deviceID, emptyModelJSON, now); err != nil {
		return fmt.Errorf("初始化配置版本历史: %w", err)
	}
	return nil
}

// ErrVersionConflict 表示提交所基于的版本已经不是当前版本（并发写冲突）。
var ErrVersionConflict = errors.New("配置版本冲突")

// CommitConfig 原子提交新模型：版本 +1、更新当前态、写版本历史。
func (s *Store) CommitConfig(ctx context.Context, deviceID int64, modelJSON, operationID string) (int64, error) {
	return s.CommitConfigCAS(ctx, deviceID, modelJSON, operationID, -1)
}

// CommitConfigCAS 是带乐观并发控制的提交：expectedVersion >= 0 时，
// 仅当当前版本等于 expectedVersion 才提交，否则返回 ErrVersionConflict。
func (s *Store) CommitConfigCAS(ctx context.Context, deviceID int64, modelJSON, operationID string, expectedVersion int64) (int64, error) {
	tx, err := s.w.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()

	var version int64
	if err := tx.QueryRowContext(ctx,
		"SELECT version FROM device_configs WHERE device_id = ?", deviceID).Scan(&version); err != nil {
		return 0, fmt.Errorf("读取模型版本: %w", err)
	}
	if expectedVersion >= 0 && version != expectedVersion {
		return 0, fmt.Errorf("%w: 期望基于版本 %d，当前版本 %d", ErrVersionConflict, expectedVersion, version)
	}
	version++
	now := nowTS()

	// 提交新模型时认定"权威态已就绪"，同步状态由快路径结果或对账刷新
	if _, err := tx.ExecContext(ctx, `
		UPDATE device_configs SET model_json = ?, version = ?, updated_at = ? WHERE device_id = ?`,
		modelJSON, version, now, deviceID); err != nil {
		return 0, fmt.Errorf("提交设备配置模型: %w", err)
	}
	var opID any
	if operationID != "" {
		opID = operationID
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO device_config_versions (device_id, version, model_json, operation_id, created_at)
		VALUES (?, ?, ?, ?, ?)`,
		deviceID, version, modelJSON, opID, now); err != nil {
		return 0, fmt.Errorf("写模型版本历史: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return version, nil
}

// RollbackConfig 把权威态回退到 failedVersion 之前一版：
// 恢复 baseModelJSON、version = failedVersion-1，并删除 failedVersion 的历史行。
//
// 调用方保证当前版本就是 failedVersion（同设备串行化），因此这里不做版本判断。
func (s *Store) RollbackConfig(ctx context.Context, deviceID, failedVersion int64, baseModelJSON string) error {
	if failedVersion <= 0 {
		return fmt.Errorf("非法回退版本: %d", failedVersion)
	}
	tx, err := s.w.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	newVersion := failedVersion - 1
	if _, err := tx.ExecContext(ctx, `
		UPDATE device_configs SET model_json = ?, version = ?, updated_at = ? WHERE device_id = ?`,
		baseModelJSON, newVersion, nowTS(), deviceID); err != nil {
		return fmt.Errorf("回退设备配置模型: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		DELETE FROM device_config_versions WHERE device_id = ? AND version = ?`,
		deviceID, failedVersion); err != nil {
		return fmt.Errorf("删除失败版本历史: %w", err)
	}
	return tx.Commit()
}

// SetSyncState 更新同步状态；reconciledAt 非空时同时刷新 last_reconciled_at。
func (s *Store) SetSyncState(ctx context.Context, deviceID int64, state string, reconciledAt *string) error {
	if reconciledAt != nil {
		_, err := s.w.ExecContext(ctx, `
			UPDATE device_configs SET sync_state = ?, last_reconciled_at = ?, updated_at = ? WHERE device_id = ?`,
			state, *reconciledAt, nowTS(), deviceID)
		return err
	}
	_, err := s.w.ExecContext(ctx,
		"UPDATE device_configs SET sync_state = ?, updated_at = ? WHERE device_id = ?",
		state, nowTS(), deviceID)
	return err
}

// ListConfigs 返回全部设备模型（启动加载）。
func (s *Store) ListConfigs(ctx context.Context) ([]*ConfigEntry, error) {
	rows, err := s.r.QueryContext(ctx, "SELECT "+configCols+" FROM device_configs")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*ConfigEntry{}
	for rows.Next() {
		ce, err := scanConfig(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, ce)
	}
	return out, rows.Err()
}

// SetVerified 记录"设备已验证收敛到 version"。
// 只前进不后退：乱序的验证结果不会把 verified 拉回旧值。
func (s *Store) SetVerified(ctx context.Context, deviceID, version int64) error {
	_, err := s.w.ExecContext(ctx, `
		UPDATE device_configs
		SET verified_version = ?, verified_at = ?, updated_at = ?
		WHERE device_id = ? AND verified_version < ?`,
		version, nowTS(), nowTS(), deviceID, version)
	if err != nil {
		return fmt.Errorf("更新已验证版本: %w", err)
	}
	return nil
}

// ConfigVersion 返回指定设备某个历史版本的模型 JSON。
func (s *Store) ConfigVersion(ctx context.Context, deviceID, version int64) (string, error) {
	var model string
	err := s.r.QueryRowContext(ctx,
		"SELECT model_json FROM device_config_versions WHERE device_id = ? AND version = ?",
		deviceID, version).Scan(&model)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", fmt.Errorf("查询历史版本 %d: %w", version, err)
	}
	return model, nil
}
