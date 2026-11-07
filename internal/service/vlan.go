package service

import (
	"context"
	"fmt"
	"sort"
	"strconv"

	"switchctl/internal/model"
	"switchctl/internal/store"
)

// PutVLAN 创建/更新 VLAN。
func (s *Service) PutVLAN(ctx context.Context, switchName string, id int, name, sourceIP string, expectedVersion int64) (*store.Operation, error) {
	label := strconv.Itoa(id)
	return s.applyChange(ctx, switchName, sourceIP, "vlan", label, "apply", expectedVersion,
		func(cfg *model.DeviceConfig, c model.Constraints) error {
			return cfg.UpsertVLAN(c, id, name)
		})
}

// DeleteVLAN 删除 VLAN（仅限权威模型中的对象）。
func (s *Service) DeleteVLAN(ctx context.Context, switchName string, id int, sourceIP string, expectedVersion int64) (*store.Operation, error) {
	label := strconv.Itoa(id)
	return s.applyChange(ctx, switchName, sourceIP, "vlan", label, "delete", expectedVersion,
		func(cfg *model.DeviceConfig, c model.Constraints) error {
			if _, ok := cfg.VLANs[id]; !ok {
				return errNotManaged(fmt.Sprintf(
					"VLAN %d 不在管理中（权威模型里没有该对象）：为免误删未托管配置，"+
						"控制器只删除自己创建的对象；如需清理请人工处理", id))
			}
			return cfg.DeleteVLAN(c, id)
		})
}

// ListVLANs 返回权威模型中的 VLAN 列表。
func (s *Service) ListVLANs(switchName string) ([]model.VLAN, error) {
	dev, err := s.deviceRow(switchName)
	if err != nil {
		return nil, err
	}
	cfg, _, err := s.state.Snapshot(dev.ID)
	if err != nil {
		return nil, err
	}
	out := make([]model.VLAN, 0, len(cfg.VLANs))
	for _, v := range cfg.VLANs {
		out = append(out, *v)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}
