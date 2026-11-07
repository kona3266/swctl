// Package model 定义交换机的全量配置模型（业务管理域：VRF/VLAN/SVI/BGP）、
// 业务变更操作与模型差异计算。
//
// 模型与厂商无关：不含任何命令语法、平台名称或平台策略。
// 平台差异（约束、标记编码、渲染）由方言层声明或实现：
// 平台策略以 Constraints 的形式注入，模型只消费结构、不认识具体厂商。
//
// 这是控制器的权威状态（desired state）的数据结构：内存为主、DB 持久化。
package model

// IntRange 是闭区间 [From, To]。
type IntRange struct {
	From int `json:"from"`
	To   int `json:"to"`
}

// Contains 判断 v 是否落在区间内。
func (r IntRange) Contains(v int) bool { return v >= r.From && v <= r.To }

// Constraints 是平台对业务对象的约束声明。
// 由方言层（internal/device/dialect）提供，业务层据此校验；模型本身不含厂商知识。
type Constraints struct {
	// ReservedVLANs 是平台保留、禁止业务操作的 VLAN 区间（如 1002~1005）。
	ReservedVLANs []IntRange
	// VLANNameMaxLen 是 VLAN 名称字段的最大长度（业务名的可用长度，
	// 已由方言扣除其标记编码所占空间）；0 表示不限制。
	VLANNameMaxLen int
}

// ReservedVLAN 返回 id 命中的保留区间。
func (c Constraints) ReservedVLAN(id int) (IntRange, bool) {
	for _, r := range c.ReservedVLANs {
		if r.Contains(id) {
			return r, true
		}
	}
	return IntRange{}, false
}

// DeviceConfig 是一台交换机的全量配置模型（仅业务管理域）。
// 未管理部分的配置不进入模型，也不会被控制器触碰。
type DeviceConfig struct {
	VRFs  map[string]*VRF `json:"vrfs"`
	VLANs map[int]*VLAN   `json:"vlans"`
	SVIs  map[int]*SVI    `json:"svis"` // VLAN 三层接口，key = VLAN ID
	// BGPPeers：VRF 内 BGP 邻居，key = "vrf/邻居IP"
	BGPPeers map[string]*BGPPeer `json:"bgp_peers"`
	// BGPASN 是本地 BGP AS 号（渲染上下文用，不参与收敛比对）
	BGPASN string `json:"bgp_asn,omitempty"`
}

// NewDeviceConfig 返回空模型。
func NewDeviceConfig() *DeviceConfig {
	return &DeviceConfig{
		VRFs: map[string]*VRF{}, VLANs: map[int]*VLAN{}, SVIs: map[int]*SVI{},
		BGPPeers: map[string]*BGPPeer{},
	}
}

// Clone 深拷贝。
func (c *DeviceConfig) Clone() *DeviceConfig {
	out := NewDeviceConfig()
	for k, v := range c.VRFs {
		vv := *v
		out.VRFs[k] = &vv
	}
	for k, v := range c.VLANs {
		vv := *v
		out.VLANs[k] = &vv
	}
	for k, v := range c.SVIs {
		vv := *v
		out.SVIs[k] = &vv
	}
	for k, v := range c.BGPPeers {
		vv := *v
		out.BGPPeers[k] = &vv
	}
	out.BGPASN = c.BGPASN
	return out
}
