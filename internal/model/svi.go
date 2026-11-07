package model

import (
	"fmt"
	"net"
	"net/netip"
	"strings"
)

// SVI 是 VLAN 三层接口（interface VlanN）的业务对象。
// IP 一律以 CIDR 形式存储（如 10.0.0.1/24），厂商的点分掩码形式由方言层转换。
type SVI struct {
	VLANID  int    `json:"vlan_id"`
	IP      string `json:"ip"`            // CIDR
	VRF     string `json:"vrf,omitempty"` // 绑定的 VRF（空 = 全局路由表）
	Managed bool   `json:"managed"`
}

// Equal 比较业务字段与标记。
func (s *SVI) Equal(o *SVI) bool {
	return s.VLANID == o.VLANID && s.IP == o.IP && s.VRF == o.VRF && s.Managed == o.Managed
}

// SetSVIVRF 把 SVI 加入 VRF（要求 SVI 与 VRF 都在管理中）。
func (c *DeviceConfig) SetSVIVRF(vlanID int, vrf string) error {
	svi, ok := c.SVIs[vlanID]
	if !ok {
		return fmt.Errorf("VLAN %d 的三层接口不在管理中：请先创建该接口", vlanID)
	}
	if _, ok := c.VRFs[vrf]; !ok {
		return fmt.Errorf("VRF %s 不在管理中：请先创建或纳管该 VRF", vrf)
	}
	svi.VRF = vrf
	return nil
}

// ClearSVIVRF 把 SVI 移出 VRF（若本来未绑定则为幂等空操作）。
func (c *DeviceConfig) ClearSVIVRF(vlanID int) error {
	svi, ok := c.SVIs[vlanID]
	if !ok {
		return fmt.Errorf("VLAN %d 的三层接口不在管理中：请先创建该接口", vlanID)
	}
	svi.VRF = ""
	return nil
}

// ValidateSVI 校验 SVI 参数并返回规范化后的 CIDR。
func ValidateSVI(vlanID int, ip string) (string, error) {
	if vlanID < 2 || vlanID > 4094 {
		return "", fmt.Errorf("SVI 的 VLAN ID %d 非法：有效范围 2~4094（VLAN 1 与保留 VLAN 不支持）", vlanID)
	}
	normalized, err := NormalizeCIDR(ip)
	if err != nil {
		return "", err
	}
	return normalized, nil
}

// NormalizeCIDR 把 IPv4 地址/掩码规范化为 CIDR 文本。
// 接受 "10.0.0.1/24" 与 "10.0.0.1/255.255.255.0" 两种写法。
func NormalizeCIDR(ip string) (string, error) {
	s := strings.TrimSpace(ip)
	if s == "" {
		return "", fmt.Errorf("IP 地址不能为空：格式为 CIDR，如 10.0.0.1/24")
	}
	if prefix, err := netip.ParsePrefix(s); err == nil {
		if !prefix.Addr().Is4() {
			return "", fmt.Errorf("IP %q 非法：当前仅支持 IPv4（如 10.0.0.1/24）", ip)
		}
		return prefix.String(), nil
	}
	// 点分掩码写法：10.0.0.1/255.255.255.0
	addrPart, maskPart, ok := strings.Cut(s, "/")
	if !ok {
		return "", fmt.Errorf("IP %q 非法：需要带掩码（CIDR 形式，如 10.0.0.1/24）", ip)
	}
	addr, err := netip.ParseAddr(strings.TrimSpace(addrPart))
	if err != nil || !addr.Is4() {
		return "", fmt.Errorf("IP %q 非法：仅支持 IPv4 地址", ip)
	}
	bits, ok := DottedMaskToBits(strings.TrimSpace(maskPart))
	if !ok {
		return "", fmt.Errorf("掩码 %q 非法：应为点分十进制（如 255.255.255.0）或前缀长度（如 24）", maskPart)
	}
	// 注意：不能使用 Addr.Prefix()（会把主机位清零，变成网段地址）
	return fmt.Sprintf("%s/%d", addr.String(), bits), nil
}

// DottedMaskToBits 把点分掩码转成前缀长度；掩码必须是连续的 1。
func DottedMaskToBits(dotted string) (int, bool) {
	ip := net.ParseIP(dotted)
	if ip == nil {
		return 0, false
	}
	v4 := ip.To4()
	if v4 == nil {
		return 0, false
	}
	mask := net.IPMask(v4)
	ones, bits := mask.Size()
	if bits != 32 {
		return 0, false
	}
	// Size 对非连续掩码也返回计数，需校验连续性
	if !net.IP(mask).Equal(net.IP(net.CIDRMask(ones, 32))) {
		return 0, false
	}
	return ones, true
}

// BitsToDottedMask 把前缀长度转成点分掩码。
func BitsToDottedMask(bits int) string {
	return net.IP(net.CIDRMask(bits, 32)).String()
}

// CIDRFromAddrMask 组合地址与点分掩码为 CIDR（解析设备配置用）。
func CIDRFromAddrMask(addr, dottedMask string) (string, bool) {
	bits, ok := DottedMaskToBits(dottedMask)
	if !ok {
		return "", false
	}
	a, err := netip.ParseAddr(addr)
	if err != nil || !a.Is4() {
		return "", false
	}
	// 不能用 Addr.Prefix()（会清零主机位）
	return fmt.Sprintf("%s/%d", a.String(), bits), true
}

// SplitCIDR 拆出地址与点分掩码（渲染设备命令用）。
func SplitCIDR(cidr string) (addr, dottedMask string, ok bool) {
	p, err := netip.ParsePrefix(cidr)
	if err != nil {
		return "", "", false
	}
	return p.Addr().String(), BitsToDottedMask(p.Bits()), true
}

// UpsertSVI 创建或更新 VLAN 三层接口。要求该 VLAN 在权威模型中（管理中）。
func (c *DeviceConfig) UpsertSVI(vlanID int, ip string) error {
	normalized, err := ValidateSVI(vlanID, ip)
	if err != nil {
		return err
	}
	if _, ok := c.VLANs[vlanID]; !ok {
		return fmt.Errorf("VLAN %d 不在管理中（权威模型里没有该 VLAN）："+
			"请先创建或纳管该 VLAN，再创建其三层接口", vlanID)
	}
	c.SVIs[vlanID] = &SVI{VLANID: vlanID, IP: normalized, Managed: true}
	return nil
}

// DeleteSVI 从期望模型中移除 SVI。
func (c *DeviceConfig) DeleteSVI(vlanID int) error {
	if vlanID < 2 || vlanID > 4094 {
		return fmt.Errorf("SVI 的 VLAN ID %d 非法：有效范围 2~4094", vlanID)
	}
	delete(c.SVIs, vlanID)
	return nil
}
