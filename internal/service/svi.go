package service

import (
	"context"
	"fmt"
	"sort"
	"strconv"

	"switchctl/internal/model"
	"switchctl/internal/store"
)

// PutSVI 创建/更新 VLAN 三层接口（IP 必填，CIDR 形式）。该 VLAN 需在管理中。
func (s *Service) PutSVI(ctx context.Context, switchName string, vlanID int, ip, sourceIP string, expectedVersion int64) (*store.Operation, error) {
	return s.applyChange(ctx, switchName, sourceIP, "svi", strconv.Itoa(vlanID), "apply", expectedVersion,
		func(cfg *model.DeviceConfig, _ model.Constraints) error {
			return cfg.UpsertSVI(vlanID, ip)
		})
}

// DeleteSVI 删除 VLAN 三层接口（仅限管理中的对象）。
func (s *Service) DeleteSVI(ctx context.Context, switchName string, vlanID int, sourceIP string, expectedVersion int64) (*store.Operation, error) {
	return s.applyChange(ctx, switchName, sourceIP, "svi", strconv.Itoa(vlanID), "delete", expectedVersion,
		func(cfg *model.DeviceConfig, _ model.Constraints) error {
			if _, ok := cfg.SVIs[vlanID]; !ok {
				return errNotManaged(fmt.Sprintf(
					"VLAN %d 的三层接口不在管理中（权威模型里没有该对象）："+
						"为免误删未托管配置，控制器只删除自己创建的对象；如需清理请人工处理", vlanID))
			}
			return cfg.DeleteSVI(vlanID)
		})
}

// ListSVIs 返回权威模型中的 SVI 列表。
func (s *Service) ListSVIs(switchName string) ([]model.SVI, error) {
	dev, err := s.deviceRow(switchName)
	if err != nil {
		return nil, err
	}
	cfg, _, err := s.state.Snapshot(dev.ID)
	if err != nil {
		return nil, err
	}
	out := make([]model.SVI, 0, len(cfg.SVIs))
	for _, v := range cfg.SVIs {
		out = append(out, *v)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].VLANID < out[j].VLANID })
	return out, nil
}

// JoinSVIVRF 把 SVI 加入 VRF（要求两者都在管理中）。
// 注意：切换接口所属 VRF 往往会导致接口 IP 被清空；重新下发 IP 是 southbound
// 后端在应用差异时必须保证的语义，不属于业务层职责。
func (s *Service) JoinSVIVRF(ctx context.Context, switchName string, vlanID int, vrf, sourceIP string, expectedVersion int64) (*store.Operation, error) {
	return s.applyChange(ctx, switchName, sourceIP, "svi-vrf", strconv.Itoa(vlanID), "apply", expectedVersion,
		func(cfg *model.DeviceConfig, _ model.Constraints) error {
			return cfg.SetSVIVRF(vlanID, vrf)
		})
}

// LeaveSVIVRF 把 SVI 移出 VRF（未绑定时为幂等空操作）。
func (s *Service) LeaveSVIVRF(ctx context.Context, switchName string, vlanID int, sourceIP string, expectedVersion int64) (*store.Operation, error) {
	return s.applyChange(ctx, switchName, sourceIP, "svi-vrf", strconv.Itoa(vlanID), "delete", expectedVersion,
		func(cfg *model.DeviceConfig, _ model.Constraints) error {
			return cfg.ClearSVIVRF(vlanID)
		})
}
