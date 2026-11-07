package model

import (
	"fmt"
	"net/netip"
	"strconv"
	"strings"
)

// BGPPeer 是某个 VRF 内的一个 BGP 邻居（业务对象）。
type BGPPeer struct {
	VRF      string `json:"vrf"`
	Neighbor string `json:"neighbor"` // 邻居 IPv4 地址
	RemoteAS string `json:"remote_as"`
	// Activate 表示邻居已在 address-family 中激活（未激活的邻居不生效）
	Activate bool `json:"activate"`
	Managed  bool `json:"managed"`
}

// PeerKey 返回 BGPPeers 的 map 键。
func PeerKey(vrf, neighbor string) string { return vrf + "/" + neighbor }

// Equal 比较业务字段与标记（Activate 必须为 true 才算收敛）。
func (p *BGPPeer) Equal(o *BGPPeer) bool {
	return p.VRF == o.VRF && p.Neighbor == o.Neighbor && p.RemoteAS == o.RemoteAS &&
		p.Activate == o.Activate && p.Managed == o.Managed
}

// ValidateBGPPeer 校验邻居参数。
func ValidateBGPPeer(vrf, neighbor, remoteAS string) error {
	if err := validatePeerKey(vrf, neighbor); err != nil {
		return err
	}
	if _, err := parseASN(remoteAS); err != nil {
		return err
	}
	return nil
}

func validatePeerKey(vrf, neighbor string) error {
	if !vrfNameRe.MatchString(vrf) {
		return fmt.Errorf("VRF 名称 %q 非法", vrf)
	}
	if addr, err := netip.ParseAddr(strings.TrimSpace(neighbor)); err != nil || !addr.Is4() {
		return fmt.Errorf("邻居地址 %q 非法：需要 IPv4 地址", neighbor)
	}
	return nil
}

func parseASN(remoteAS string) (uint64, error) {
	asn, err := strconv.ParseUint(strings.TrimSpace(remoteAS), 10, 32)
	if err != nil || asn == 0 {
		return 0, fmt.Errorf("对端 AS 号 %q 非法：需要 1~4294967295 的整数", remoteAS)
	}
	return asn, nil
}

// UpsertBGPPeer 创建或更新 VRF 内的 BGP 邻居（要求该 VRF 在管理中）。
func (c *DeviceConfig) UpsertBGPPeer(vrf, neighbor, remoteAS string) error {
	if err := ValidateBGPPeer(vrf, neighbor, remoteAS); err != nil {
		return err
	}
	if _, ok := c.VRFs[vrf]; !ok {
		return fmt.Errorf("VRF %s 不在管理中：请先创建或纳管该 VRF", vrf)
	}
	c.BGPPeers[PeerKey(vrf, neighbor)] = &BGPPeer{
		VRF: vrf, Neighbor: neighbor, RemoteAS: remoteAS, Activate: true, Managed: true,
	}
	return nil
}

// DeleteBGPPeer 从期望模型中移除 BGP 邻居。
func (c *DeviceConfig) DeleteBGPPeer(vrf, neighbor string) error {
	if err := validatePeerKey(vrf, neighbor); err != nil {
		return err
	}
	delete(c.BGPPeers, PeerKey(vrf, neighbor))
	return nil
}
