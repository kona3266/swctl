package model

import (
	"fmt"
	"net/netip"
	"strconv"
	"strings"
)

// BGP 邻居参数的取值范围。
const (
	// MaxBGPSecretLen 是 BGP TCP-MD5 密钥的最大长度（NX-OS / RGOS 均为 80）。
	MaxBGPSecretLen = 80
	// MaskedSecret 是密码类字段在 API 与审计输出中的占位符。
	MaskedSecret = "***"

	bfdTimerMinMS    = 50
	bfdTimerMaxMS    = 30000
	bfdMultiplierMin = 1
	bfdMultiplierMax = 50

	maxPrefixLimit = int64(4294967295)
)

// BGPPeer 是某个 VRF 内的一个 BGP 邻居（业务对象）。
type BGPPeer struct {
	VRF      string `json:"vrf"`
	Neighbor string `json:"neighbor"` // 邻居 IPv4 地址
	RemoteAS string `json:"remote_as"`
	// SourceIP 是本端与邻居建连使用的源地址（update-source）。
	// 空表示使用平台默认（通常为出接口地址）。
	SourceIP string `json:"source_ip,omitempty"`
	// Password 是 BGP TCP-MD5 密钥。
	// 设备不回显该密钥，因此它属于“只写”字段：参与下发差异，但不参与收敛验证
	// （见 ComputeVerifiable）。
	Password string `json:"password,omitempty"`
	// BFD 及定时器。定时器为 0 表示使用平台默认值；任一定时器非 0 时必须开启 BFD。
	BFD           bool `json:"bfd,omitempty"`
	BFDMinRxMS    int  `json:"bfd_min_rx_ms,omitempty"`
	BFDMinTxMS    int  `json:"bfd_min_tx_ms,omitempty"`
	BFDMultiplier int  `json:"bfd_multiplier,omitempty"`
	// MaxPrefix 是允许从该邻居接收的最大前缀数；0 表示不限制。
	MaxPrefix int `json:"max_prefix,omitempty"`
	// MaxPrefixWarningOnly 表示只告警不断开，仅在 MaxPrefix > 0 时有意义。
	MaxPrefixWarningOnly bool `json:"max_prefix_warning_only,omitempty"`
	// RouteReflectorClient 表示该邻居是本机（路由反射器）的客户端；只能用于 iBGP。
	RouteReflectorClient bool `json:"route_reflector_client,omitempty"`
	// Activate 表示邻居已在 address-family 中激活（未激活的邻居不生效）
	Activate bool `json:"activate"`
	Managed  bool `json:"managed"`
}

// BGPPeerParams 是创建/更新邻居时的参数。
//
// 字段为 nil 表示“本次不修改”；非 nil 表示显式设置，因此可以显式清空
// （如 Password 传空串清除密钥、MaxPrefix 传 0 取消限制）。
type BGPPeerParams struct {
	RemoteAS             *string
	SourceIP             *string
	Password             *string
	BFD                  *bool
	BFDMinRxMS           *int
	BFDMinTxMS           *int
	BFDMultiplier        *int
	MaxPrefix            *int
	MaxPrefixWarningOnly *bool
	RouteReflectorClient *bool
}

// applyTo 把非 nil 的参数写到 peer 上（不做校验）。
func (p BGPPeerParams) applyTo(peer *BGPPeer) {
	if p.RemoteAS != nil {
		peer.RemoteAS = strings.TrimSpace(*p.RemoteAS)
	}
	if p.SourceIP != nil {
		peer.SourceIP = strings.TrimSpace(*p.SourceIP)
	}
	if p.Password != nil {
		peer.Password = *p.Password
	}
	if p.BFD != nil {
		peer.BFD = *p.BFD
	}
	if p.BFDMinRxMS != nil {
		peer.BFDMinRxMS = *p.BFDMinRxMS
	}
	if p.BFDMinTxMS != nil {
		peer.BFDMinTxMS = *p.BFDMinTxMS
	}
	if p.BFDMultiplier != nil {
		peer.BFDMultiplier = *p.BFDMultiplier
	}
	if p.MaxPrefix != nil {
		peer.MaxPrefix = *p.MaxPrefix
	}
	if p.MaxPrefixWarningOnly != nil {
		peer.MaxPrefixWarningOnly = *p.MaxPrefixWarningOnly
	}
	if p.RouteReflectorClient != nil {
		peer.RouteReflectorClient = *p.RouteReflectorClient
	}
}

// PeerKey 返回 BGPPeers 的 map 键。
func PeerKey(vrf, neighbor string) string { return vrf + "/" + neighbor }

// Equal 比较全部业务字段（含密码）：用于下发差异。
func (p *BGPPeer) Equal(o *BGPPeer) bool { return p.equal(o, true) }

// EqualVerifiable 忽略设备不可回读的只写字段（密码）：用于收敛验证。
func (p *BGPPeer) EqualVerifiable(o *BGPPeer) bool { return p.equal(o, false) }

func (p *BGPPeer) equal(o *BGPPeer, withSecret bool) bool {
	if p == nil || o == nil {
		return p == o
	}
	if p.VRF != o.VRF || p.Neighbor != o.Neighbor || p.RemoteAS != o.RemoteAS ||
		p.SourceIP != o.SourceIP ||
		p.BFD != o.BFD || p.BFDMinRxMS != o.BFDMinRxMS || p.BFDMinTxMS != o.BFDMinTxMS ||
		p.BFDMultiplier != o.BFDMultiplier || p.MaxPrefix != o.MaxPrefix ||
		p.MaxPrefixWarningOnly != o.MaxPrefixWarningOnly ||
		p.RouteReflectorClient != o.RouteReflectorClient ||
		p.Activate != o.Activate || p.Managed != o.Managed {
		return false
	}
	if withSecret && p.Password != o.Password {
		return false
	}
	return true
}

// masked 返回把密码替换为占位符的副本（审计/API 输出用）。
func (p BGPPeer) masked() BGPPeer {
	if p.Password != "" {
		p.Password = MaskedSecret
	}
	return p
}

// UpsertBGPPeer 创建或更新 VRF 内的 BGP 邻居（要求该 VRF 在管理中）。
//
// 更新语义是“按参数局部修改”：未给出的字段保持原值；给出即覆盖（含显式清空）。
func (c *DeviceConfig) UpsertBGPPeer(vrf, neighbor string, params BGPPeerParams) error {
	if err := validatePeerKey(vrf, neighbor); err != nil {
		return err
	}
	if _, ok := c.VRFs[vrf]; !ok {
		return fmt.Errorf("VRF %s 不在管理中：请先创建或纳管该 VRF", vrf)
	}

	key := PeerKey(vrf, neighbor)
	peer := &BGPPeer{VRF: vrf, Neighbor: neighbor, Activate: true, Managed: true}
	if cur, ok := c.BGPPeers[key]; ok {
		// 在副本上修改，校验失败时不污染权威模型。
		cp := *cur
		peer = &cp
	}
	params.applyTo(peer)
	if err := validatePeer(peer); err != nil {
		return err
	}
	if peer.RouteReflectorClient && c.BGPASN != "" && peer.RemoteAS != c.BGPASN {
		return fmt.Errorf("route_reflector_client 只能用于 iBGP 邻居："+
			"对端 AS %s 与本地 AS %s 不同", peer.RemoteAS, c.BGPASN)
	}
	c.BGPPeers[key] = peer
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

func validatePeerKey(vrf, neighbor string) error {
	if !vrfNameRe.MatchString(vrf) {
		return fmt.Errorf("VRF 名称 %q 非法", vrf)
	}
	if addr, err := netip.ParseAddr(strings.TrimSpace(neighbor)); err != nil || !addr.Is4() {
		return fmt.Errorf("邻居地址 %q 非法：需要 IPv4 地址", neighbor)
	}
	return nil
}

// validatePeer 校验邻居参数（合并后的完整对象）。
func validatePeer(p *BGPPeer) error {
	if p.RemoteAS == "" {
		return fmt.Errorf("创建 BGP 邻居需要 remote_as（对端 AS 号）")
	}
	if _, err := parseASN(p.RemoteAS); err != nil {
		return err
	}
	if p.SourceIP != "" {
		if err := validateSourceIP(p.SourceIP); err != nil {
			return err
		}
	}
	if p.Password != "" {
		if err := validateBGPSecret(p.Password); err != nil {
			return err
		}
	}
	if err := validateBFD(p); err != nil {
		return err
	}
	if int64(p.MaxPrefix) < 0 || int64(p.MaxPrefix) > maxPrefixLimit {
		return fmt.Errorf("max_prefix %d 非法：需要 0（不限制）或 1~%d", p.MaxPrefix, maxPrefixLimit)
	}
	if p.MaxPrefixWarningOnly && p.MaxPrefix == 0 {
		return fmt.Errorf("max_prefix_warning_only 需要同时设置 max_prefix")
	}
	return nil
}

// validateSourceIP 校验本端源地址：必须是可用的单播 IPv4。
func validateSourceIP(s string) error {
	addr, err := netip.ParseAddr(s)
	if err != nil || !addr.Is4() {
		return fmt.Errorf("source_ip %q 非法：需要 IPv4 地址（如 10.0.0.1）", s)
	}
	if addr.IsUnspecified() || addr.IsMulticast() || addr.IsLoopback() {
		return fmt.Errorf("source_ip %q 非法：需要可用的单播 IPv4 地址", s)
	}
	return nil
}

func validateBGPSecret(s string) error {
	if len(s) > MaxBGPSecretLen {
		return fmt.Errorf("BGP 密码过长：最多 %d 个字符", MaxBGPSecretLen)
	}
	for i := 0; i < len(s); i++ {
		if s[i] < 0x21 || s[i] > 0x7e {
			return fmt.Errorf("BGP 密码只能包含可打印 ASCII 字符（不含空格与控制字符）")
		}
	}
	return nil
}

func validateBFD(p *BGPPeer) error {
	for _, f := range []struct {
		name string
		val  int
	}{
		{"bfd_min_rx_ms", p.BFDMinRxMS},
		{"bfd_min_tx_ms", p.BFDMinTxMS},
	} {
		if f.val == 0 {
			continue
		}
		if f.val < bfdTimerMinMS || f.val > bfdTimerMaxMS {
			return fmt.Errorf("%s=%d 非法：需要 0（平台默认）或 %d~%d 毫秒",
				f.name, f.val, bfdTimerMinMS, bfdTimerMaxMS)
		}
		if !p.BFD {
			return fmt.Errorf("%s 需要同时开启 bfd", f.name)
		}
	}
	if p.BFDMultiplier == 0 {
		return nil
	}
	if p.BFDMultiplier < bfdMultiplierMin || p.BFDMultiplier > bfdMultiplierMax {
		return fmt.Errorf("bfd_multiplier=%d 非法：需要 0（平台默认）或 %d~%d",
			p.BFDMultiplier, bfdMultiplierMin, bfdMultiplierMax)
	}
	if !p.BFD {
		return fmt.Errorf("bfd_multiplier 需要同时开启 bfd")
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
