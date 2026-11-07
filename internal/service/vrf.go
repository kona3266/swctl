package service

import (
	"context"
	"fmt"
	"sort"

	"switchctl/internal/model"
	"switchctl/internal/store"
)

// PutVRF 创建/更新 VRF。
func (s *Service) PutVRF(ctx context.Context, switchName, vrfName, rd, sourceIP string, expectedVersion int64) (*store.Operation, error) {
	return s.applyChange(ctx, switchName, sourceIP, "vrf", vrfName, "apply", expectedVersion,
		func(cfg *model.DeviceConfig, _ model.Constraints) error {
			return cfg.UpsertVRF(vrfName, rd)
		})
}

// DeleteVRF 删除 VRF（仅限权威模型中的对象）。
func (s *Service) DeleteVRF(ctx context.Context, switchName, vrfName, sourceIP string, expectedVersion int64) (*store.Operation, error) {
	return s.applyChange(ctx, switchName, sourceIP, "vrf", vrfName, "delete", expectedVersion,
		func(cfg *model.DeviceConfig, _ model.Constraints) error {
			if _, ok := cfg.VRFs[vrfName]; !ok {
				return errNotManaged(fmt.Sprintf(
					"VRF %s 不在管理中（权威模型里没有该对象）：为免误删未托管配置，"+
						"控制器只删除自己创建的对象；如需清理请人工处理", vrfName))
			}
			return cfg.DeleteVRF(vrfName)
		})
}

// ListVRFs 返回权威模型中的 VRF 列表。
func (s *Service) ListVRFs(switchName string) ([]model.VRF, error) {
	dev, err := s.deviceRow(switchName)
	if err != nil {
		return nil, err
	}
	cfg, _, err := s.state.Snapshot(dev.ID)
	if err != nil {
		return nil, err
	}
	out := make([]model.VRF, 0, len(cfg.VRFs))
	for _, v := range cfg.VRFs {
		out = append(out, *v)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}
