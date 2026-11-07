package service

import (
	"context"
	"fmt"
	"sort"

	"switchctl/internal/model"
	"switchctl/internal/store"
)

// PutBGPNeighbor 配置 VRF 内的 BGP 邻居（该 VRF 需在管理中）。
func (s *Service) PutBGPNeighbor(ctx context.Context, switchName, vrf, neighbor, remoteAS, sourceIP string, expectedVersion int64) (*store.Operation, error) {
	return s.applyChange(ctx, switchName, sourceIP, "bgp-neighbor", vrf+"/"+neighbor, "apply", expectedVersion,
		func(cfg *model.DeviceConfig, _ model.Constraints) error {
			// 本地 AS：设备清单声明兜底（设备已有 BGP 时解析值优先）
			if cfg.BGPASN == "" {
				if dev, ok := s.cfg.DeviceByName(switchName); ok {
					cfg.BGPASN = dev.BGPASN
				}
			}
			if cfg.BGPASN == "" {
				return errInvalid("无法确定本地 BGP AS 号：设备未运行 BGP，" +
					"请在设备清单中配置 bgp_asn")
			}
			return cfg.UpsertBGPPeer(vrf, neighbor, remoteAS)
		})
}

// DeleteBGPNeighbor 删除 VRF 内的 BGP 邻居（仅限管理中的对象）。
func (s *Service) DeleteBGPNeighbor(ctx context.Context, switchName, vrf, neighbor, sourceIP string, expectedVersion int64) (*store.Operation, error) {
	return s.applyChange(ctx, switchName, sourceIP, "bgp-neighbor", vrf+"/"+neighbor, "delete", expectedVersion,
		func(cfg *model.DeviceConfig, _ model.Constraints) error {
			if _, ok := cfg.BGPPeers[model.PeerKey(vrf, neighbor)]; !ok {
				return errNotManaged(fmt.Sprintf(
					"VRF %s 中的 BGP 邻居 %s 不在管理中（权威模型里没有该对象）："+
						"为免误删未托管配置，控制器只删除自己创建的对象；如需清理请人工处理", vrf, neighbor))
			}
			return cfg.DeleteBGPPeer(vrf, neighbor)
		})
}

// ListBGPNeighbors 返回权威模型中某 VRF 的 BGP 邻居列表。
func (s *Service) ListBGPNeighbors(switchName, vrf string) ([]model.BGPPeer, error) {
	dev, err := s.deviceRow(switchName)
	if err != nil {
		return nil, err
	}
	cfg, _, err := s.state.Snapshot(dev.ID)
	if err != nil {
		return nil, err
	}
	out := make([]model.BGPPeer, 0)
	for _, p := range cfg.BGPPeers {
		if p.VRF == vrf {
			out = append(out, *p)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Neighbor < out[j].Neighbor })
	return out, nil
}
