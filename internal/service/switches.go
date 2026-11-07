package service

import (
	"context"
	"errors"
	"time"

	"switchctl/internal/store"
)

// SwitchInfo 是交换机列表的业务视角信息。
type SwitchInfo struct {
	Name      string `json:"name"`
	Dialect   string `json:"dialect"`
	SyncState string `json:"sync_state"`
	// Version 是期望态（desired）版本；VerifiedVersion 是最后验证落地的版本。
	// Version > VerifiedVersion 表示存在尚未落到设备的变更。
	Version          int64      `json:"version"`
	VerifiedVersion  int64      `json:"verified_version"`
	VerifiedAt       *time.Time `json:"verified_at,omitempty"`
	LastReconciledAt *time.Time `json:"last_reconciled_at,omitempty"`
	LastSeenAt       *time.Time `json:"last_seen_at,omitempty"`
	LastError        string     `json:"last_error,omitempty"`
}

// ListSwitches 返回 YAML 清单中的交换机（含权威态元信息）。
// 已从清单移除的设备不会被列出，但其历史操作仍保留在 DB。
func (s *Service) ListSwitches(ctx context.Context) ([]SwitchInfo, error) {
	out := make([]SwitchInfo, 0, len(s.cfg.Devices))
	for i := range s.cfg.Devices {
		cfgDev := &s.cfg.Devices[i]
		d, err := s.store.GetDeviceByName(ctx, cfgDev.Name)
		if errors.Is(err, store.ErrNotFound) {
			continue // 启动时已同步；异常情况下跳过而不是伪造一条记录
		}
		if err != nil {
			return nil, err
		}
		item := SwitchInfo{
			Name: d.Name, Dialect: d.Dialect,
			LastSeenAt: d.LastSeenAt, LastError: d.LastError,
		}
		if v, ver, verified, verifiedAt, at, err := s.state.Status(d.ID); err == nil {
			item.SyncState, item.Version, item.LastReconciledAt = v, ver, at
			item.VerifiedVersion, item.VerifiedAt = verified, verifiedAt
		} else {
			item.SyncState = store.SyncUnknown
		}
		out = append(out, item)
	}
	return out, nil
}

func (s *Service) deviceRow(name string) (*store.Device, error) {
	// YAML 是设备清单的唯一事实来源：已移除的设备即使 DB 有历史行也不可再操作。
	if _, ok := s.cfg.DeviceByName(name); !ok {
		return nil, errNotFound("交换机不存在: " + name)
	}
	dev, err := s.store.GetDeviceByName(context.Background(), name)
	if err != nil {
		return nil, errNotFound("交换机不存在: " + name)
	}
	return dev, nil
}

// QueueDepth 是单台设备的待执行队列长度（指标用）。
type QueueDepth struct {
	Name  string
	Depth int
}

// QueueDepths 返回各设备队列深度。
func (s *Service) QueueDepths() []QueueDepth {
	out := make([]QueueDepth, 0, len(s.cfg.Devices))
	for i := range s.cfg.Devices {
		name := s.cfg.Devices[i].Name
		d, err := s.store.GetDeviceByName(context.Background(), name)
		if err != nil {
			continue
		}
		out = append(out, QueueDepth{Name: name, Depth: s.runner.QueueDepth(d.ID)})
	}
	return out
}

// ListOperations 是操作列表的门面（指标与运维查询共用）。
func (s *Service) ListOperationsPage(ctx context.Context, limit int) ([]*store.Operation, error) {
	return s.store.ListOperations(ctx, store.OperationFilter{Limit: limit})
}
