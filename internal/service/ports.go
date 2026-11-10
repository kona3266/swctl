package service

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"switchctl/internal/model"
	"switchctl/internal/store"
)

// ListPorts 返回权威模型中受管物理口列表。
func (s *Service) ListPorts(switchName string) ([]model.Port, error) {
	dev, err := s.deviceRow(switchName)
	if err != nil {
		return nil, err
	}
	cfg, _, err := s.state.Snapshot(dev.ID)
	if err != nil {
		return nil, err
	}
	out := make([]model.Port, 0, len(cfg.Ports))
	for _, p := range cfg.Ports {
		out = append(out, *p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// PutPort 纳管物理口并设置二层/三层模式。
// mode 为空时：新建默认 l3，已存在表示不修改；切换二层用 mode=l2（对应 switchport）。
func (s *Service) PutPort(ctx context.Context, switchName, port, mode, sourceIP string, expectedVersion int64) (*store.Operation, error) {
	port = strings.TrimSpace(port)
	return s.applyChange(ctx, switchName, sourceIP, "port", port, "apply", expectedVersion,
		func(cfg *model.DeviceConfig, _ model.Constraints) error {
			return cfg.UpsertPort(port, mode)
		})
}

// ListSubInterfaces 返回某物理口下受管的子接口。
func (s *Service) ListSubInterfaces(switchName, port string) ([]model.SubInterface, error) {
	dev, err := s.deviceRow(switchName)
	if err != nil {
		return nil, err
	}
	cfg, _, err := s.state.Snapshot(dev.ID)
	if err != nil {
		return nil, err
	}
	port = strings.TrimSpace(port)
	out := make([]model.SubInterface, 0)
	for _, si := range cfg.SubInterfaces {
		if si.Port == port {
			out = append(out, *si)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].VLAN < out[j].VLAN })
	return out, nil
}

// PutSubInterface 创建/更新物理口上的 VLAN 子接口（父口须为受管三层口）。
// vrf 为 nil 表示不修改绑定；指向空串表示移回全局路由表。
func (s *Service) PutSubInterface(ctx context.Context, switchName, port string, vlan int, ip string, vrf *string, sourceIP string, expectedVersion int64) (*store.Operation, error) {
	port = strings.TrimSpace(port)
	return s.applyChange(ctx, switchName, sourceIP, "sub-interface",
		model.SubInterfaceKey(port, vlan), "apply", expectedVersion,
		func(cfg *model.DeviceConfig, _ model.Constraints) error {
			return cfg.UpsertSubInterface(port, vlan, ip, vrf)
		})
}

// JoinSubInterfaceVRF 把子接口加入 VRF（要求两者都在管理中）。
// 与 SVI 一致：切换所属 VRF 可能导致设备侧接口 IP 被清空，重新下发 IP 由
// southbound 后端在应用差异时保证。
func (s *Service) JoinSubInterfaceVRF(ctx context.Context, switchName, port string, vlan int, vrf, sourceIP string, expectedVersion int64) (*store.Operation, error) {
	port = strings.TrimSpace(port)
	return s.applyChange(ctx, switchName, sourceIP, "sub-interface-vrf",
		model.SubInterfaceKey(port, vlan), "apply", expectedVersion,
		func(cfg *model.DeviceConfig, _ model.Constraints) error {
			return cfg.SetSubInterfaceVRF(port, vlan, vrf)
		})
}

// LeaveSubInterfaceVRF 把子接口移出 VRF（未绑定时为幂等空操作）。
func (s *Service) LeaveSubInterfaceVRF(ctx context.Context, switchName, port string, vlan int, sourceIP string, expectedVersion int64) (*store.Operation, error) {
	port = strings.TrimSpace(port)
	return s.applyChange(ctx, switchName, sourceIP, "sub-interface-vrf",
		model.SubInterfaceKey(port, vlan), "delete", expectedVersion,
		func(cfg *model.DeviceConfig, _ model.Constraints) error {
			return cfg.ClearSubInterfaceVRF(port, vlan)
		})
}

// DeleteSubInterface 删除子接口（仅限管理中的对象）。
func (s *Service) DeleteSubInterface(ctx context.Context, switchName, port string, vlan int, sourceIP string, expectedVersion int64) (*store.Operation, error) {
	port = strings.TrimSpace(port)
	key := model.SubInterfaceKey(port, vlan)
	return s.applyChange(ctx, switchName, sourceIP, "sub-interface", key, "delete", expectedVersion,
		func(cfg *model.DeviceConfig, _ model.Constraints) error {
			if _, ok := cfg.SubInterfaces[key]; !ok {
				return errNotManaged(fmt.Sprintf(
					"子接口 %s 不在管理中（权威模型里没有该对象）："+
						"为免误删未托管配置，控制器只删除自己创建的对象；如需清理请人工处理", key))
			}
			return cfg.DeleteSubInterface(port, vlan)
		})
}
