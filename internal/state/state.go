// Package state 维护控制器的权威配置状态：内存为主（读快）、
// DB 持久化（重启后完整恢复）。每台交换机一份全量配置模型（业务管理域）。
//
// 一致性约定：
//   - Commit 先落库（事务成功）再替换内存；落库失败则内存不动，调用方报错。
//   - 提交是全局串行的（写入锁），保证同一设备/多设备的变更顺序确定。
//   - 对外返回的模型一律是深拷贝，调用方修改不会影响权威态。
package state

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"switchctl/internal/model"
	"switchctl/internal/store"
)

// Entry 是单台设备的权威态。
//
// desired 与 verified 分离：
//   - Version 是最后提交的期望版本（desired）；
//   - VerifiedVersion 是最后一次"设备已验证收敛"的版本。
//     VerifiedVersion < Version 即存在尚未落到设备的变更。
type Entry struct {
	DeviceID         int64
	Config           *model.DeviceConfig
	Version          int64
	VerifiedVersion  int64
	VerifiedAt       *time.Time
	SyncState        string
	LastReconciledAt *time.Time
}

// Manager 持有全部设备的权威态。
type Manager struct {
	mu       sync.RWMutex
	byDevice map[int64]*Entry

	store *store.Store
	log   *slog.Logger
}

// New 从 DB 加载全部设备模型到内存。
func New(ctx context.Context, db *store.Store, log *slog.Logger) (*Manager, error) {
	m := &Manager{byDevice: map[int64]*Entry{}, store: db, log: log}
	entries, err := db.ListConfigs(ctx)
	if err != nil {
		return nil, fmt.Errorf("加载设备配置模型: %w", err)
	}
	for _, ce := range entries {
		cfg, err := decode(ce.ModelJSON)
		if err != nil {
			return nil, fmt.Errorf("设备 %d 的模型 JSON 损坏: %w", ce.DeviceID, err)
		}
		m.byDevice[ce.DeviceID] = &Entry{
			DeviceID:         ce.DeviceID,
			Config:           cfg,
			Version:          ce.Version,
			VerifiedVersion:  ce.VerifiedVersion,
			VerifiedAt:       ce.VerifiedAt,
			SyncState:        ce.SyncState,
			LastReconciledAt: ce.LastReconciledAt,
		}
	}
	log.Info("权威配置状态已加载", "devices", len(m.byDevice))
	return m, nil
}

// Ensure 保证设备在内存与 DB 中都有一份模型（新交换机默认空模型，不导入设备配置）。
func (m *Manager) Ensure(ctx context.Context, deviceID int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.byDevice[deviceID]; ok {
		return nil
	}
	empty := model.NewDeviceConfig()
	raw, _ := json.Marshal(empty)
	if err := m.store.EnsureConfig(ctx, deviceID, string(raw)); err != nil {
		return err
	}
	m.byDevice[deviceID] = &Entry{
		DeviceID:  deviceID,
		Config:    empty,
		Version:   0,
		SyncState: store.SyncUnknown,
	}
	return nil
}

// Snapshot 返回设备权威模型的深拷贝与版本号。
func (m *Manager) Snapshot(deviceID int64) (*model.DeviceConfig, int64, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	e, ok := m.byDevice[deviceID]
	if !ok {
		return nil, 0, store.ErrNotFound
	}
	return e.Config.Clone(), e.Version, nil
}

// Status 返回设备的同步状态摘要。
func (m *Manager) Status(deviceID int64) (syncState string, version int64, verified int64, verifiedAt, lastReconciled *time.Time, err error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	e, ok := m.byDevice[deviceID]
	if !ok {
		return "", 0, 0, nil, nil, store.ErrNotFound
	}
	return e.SyncState, e.Version, e.VerifiedVersion, e.VerifiedAt, e.LastReconciledAt, nil
}

// MarkVerified 记录"设备已收敛到 version"。
// 只前进不后退：乱序到达的验证结果不会把 verified 拉回旧值。
func (m *Manager) MarkVerified(ctx context.Context, deviceID, version int64) error {
	if err := m.store.SetVerified(ctx, deviceID, version); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if e, ok := m.byDevice[deviceID]; ok && version > e.VerifiedVersion {
		e.VerifiedVersion = version
		now := time.Now()
		e.VerifiedAt = &now
	}
	return nil
}

// Commit 提交新的权威模型：先落库（版本 +1 + 历史），成功后替换内存。
// 返回新版本号。
func (m *Manager) Commit(ctx context.Context, deviceID int64, cfg *model.DeviceConfig, operationID string) (int64, error) {
	return m.CommitCAS(ctx, deviceID, cfg, operationID, -1)
}

// CommitCAS 是带乐观并发控制的提交：expectedVersion >= 0 时要求当前版本匹配，
// 否则返回 store.ErrVersionConflict（调用方应映射为 409）。
func (m *Manager) CommitCAS(ctx context.Context, deviceID int64, cfg *model.DeviceConfig, operationID string, expectedVersion int64) (int64, error) {
	raw, err := json.Marshal(cfg)
	if err != nil {
		return 0, fmt.Errorf("序列化配置模型: %w", err)
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.byDevice[deviceID]; !ok {
		return 0, store.ErrNotFound
	}
	version, err := m.store.CommitConfigCAS(ctx, deviceID, string(raw), operationID, expectedVersion)
	if err != nil {
		return 0, err
	}
	e := m.byDevice[deviceID]
	e.Config = cfg.Clone()
	e.Version = version
	return version, nil
}

// Rollback 在 apply 失败时把权威态回退到操作之前一版。
func (m *Manager) Rollback(ctx context.Context, deviceID, failedVersion int64, base *model.DeviceConfig) error {
	raw, err := json.Marshal(base)
	if err != nil {
		return fmt.Errorf("序列化配置模型: %w", err)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.byDevice[deviceID]
	if !ok {
		return store.ErrNotFound
	}
	if err := m.store.RollbackConfig(ctx, deviceID, failedVersion, string(raw)); err != nil {
		return err
	}
	e.Config = base.Clone()
	e.Version = failedVersion - 1
	return nil
}

// SetSync 更新同步状态（对账结果 / 快路径推送结果）。
func (m *Manager) SetSync(ctx context.Context, deviceID int64, syncState string, reconciledAt *time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.byDevice[deviceID]
	if !ok {
		return store.ErrNotFound
	}
	var raw *string
	if reconciledAt != nil {
		s := store.FormatTime(*reconciledAt)
		raw = &s
	}
	if err := m.store.SetSyncState(ctx, deviceID, syncState, raw); err != nil {
		return err
	}
	e.SyncState = syncState
	if reconciledAt != nil {
		e.LastReconciledAt = reconciledAt
	}
	return nil
}

func decode(modelJSON string) (*model.DeviceConfig, error) {
	cfg := model.NewDeviceConfig()
	if err := json.Unmarshal([]byte(modelJSON), cfg); err != nil {
		return nil, err
	}
	if cfg.VRFs == nil {
		cfg.VRFs = map[string]*model.VRF{}
	}
	if cfg.VLANs == nil {
		cfg.VLANs = map[int]*model.VLAN{}
	}
	if cfg.SVIs == nil {
		cfg.SVIs = map[int]*model.SVI{}
	}
	if cfg.BGPPeers == nil {
		cfg.BGPPeers = map[string]*model.BGPPeer{}
	}
	return cfg, nil
}
