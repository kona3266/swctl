package model

import (
	"sort"
)

// Diff 是权威模型与设备实际状态的差异（收敛 diff）。
//
// 删除规则（设备侧标记是唯一依据）：
//   - 设备上有、带标记、期望里没有  → Delete（清理我们自己的遗留对象）
//   - 设备上有、**无标记**、期望里没有 → Unmanaged（只报告，绝不删）
//   - 期望里有、设备上没有           → Add
//   - 两边都有但字段/标记不同        → Update（含补写标记）
//
// 应用顺序由后端保证：端口 Add/Update → 子接口 Add/Update → 子接口 Delete；
// 否则会出现“子接口先于父端口”的悬空下发。端口对象不支持删除（改模式用 PutPort）。
type Diff struct {
	VRFsAdd    []VRF
	VRFsUpdate []VRFChange // 带旧值（回滚/审计需要知道改前是什么）
	VRFsDelete []VRF
	// UnmanagedVRFs 是设备上存在但不归我们管理的 VRF（仅报告）
	UnmanagedVRFs []string

	VLANsAdd    []VLAN
	VLANsUpdate []VLANChange
	VLANsDelete []VLAN
	// UnmanagedVLANs 同上
	UnmanagedVLANs []int

	SVIsAdd    []SVI
	SVIsUpdate []SVIChange // 带旧值（移出 VRF 需要知道原绑定）
	SVIsDelete []SVI
	// UnmanagedSVIs 同上
	UnmanagedSVIs []int

	PortsAdd    []Port
	PortsUpdate []PortChange
	// UnmanagedPorts 是设备上存在、但权威模型里没有的物理口（仅报告，不删除）
	UnmanagedPorts []string

	SubInterfacesAdd    []SubInterface
	SubInterfacesUpdate []SubInterfaceChange
	SubInterfacesDelete []SubInterface
	// UnmanagedSubInterfaces 是设备上无标记的子接口（端口.编号）
	UnmanagedSubInterfaces []string

	BGPPeersAdd    []BGPPeer
	BGPPeersUpdate []BGPPeerChange
	BGPPeersDelete []BGPPeer
	// UnmanagedBGPPeers 是设备上无标记的邻居（vrf/邻居IP），仅报告
	UnmanagedBGPPeers []string
	// BGPASN 是渲染 BGP 命令用的本地 AS 号（上下文，非收敛项）
	BGPASN string
}

// 四元组变更：新增/删除各自带对象，更新同时带旧值与新值，
// 这样才能对设备做"反向应用"（回滚）与"改前是什么"的审计。

// VRFChange 是一次 VRF 更新（旧值 → 新值）。
type VRFChange struct {
	From VRF `json:"from"`
	To   VRF `json:"to"`
}

// VLANChange 是一次 VLAN 更新（旧值 → 新值）。
type VLANChange struct {
	From VLAN `json:"from"`
	To   VLAN `json:"to"`
}

// BGPPeerChange 是一次 BGP 邻居更新（旧值 → 新值）。
type BGPPeerChange struct {
	From BGPPeer `json:"from"`
	To   BGPPeer `json:"to"`
}

// SVIChange 是一次 SVI 更新（旧值 → 新值）。
type SVIChange struct {
	From SVI `json:"from"`
	To   SVI `json:"to"`
}

// PortChange 是一次物理口更新（旧值 → 新值）。
type PortChange struct {
	From Port `json:"from"`
	To   Port `json:"to"`
}

// SubInterfaceChange 是一次子接口更新（旧值 → 新值）。
type SubInterfaceChange struct {
	From SubInterface `json:"from"`
	To   SubInterface `json:"to"`
}

// Sanitized 返回可用于审计与持久化的副本：把 BGP 密码替换为占位符。
// 下发用的真实值只在内存中存在，不进入操作记录与 API 输出。
func (d *Diff) Sanitized() *Diff {
	out := *d
	if len(d.BGPPeersAdd) > 0 {
		s := make([]BGPPeer, len(d.BGPPeersAdd))
		for i := range d.BGPPeersAdd {
			s[i] = d.BGPPeersAdd[i].masked()
		}
		out.BGPPeersAdd = s
	}
	if len(d.BGPPeersUpdate) > 0 {
		s := make([]BGPPeerChange, len(d.BGPPeersUpdate))
		for i, c := range d.BGPPeersUpdate {
			s[i] = BGPPeerChange{From: c.From.masked(), To: c.To.masked()}
		}
		out.BGPPeersUpdate = s
	}
	if len(d.BGPPeersDelete) > 0 {
		s := make([]BGPPeer, len(d.BGPPeersDelete))
		for i := range d.BGPPeersDelete {
			s[i] = d.BGPPeersDelete[i].masked()
		}
		out.BGPPeersDelete = s
	}
	return &out
}

// Empty 表示无需任何变更。
func (d *Diff) Empty() bool {
	return len(d.VRFsAdd) == 0 && len(d.VRFsUpdate) == 0 && len(d.VRFsDelete) == 0 &&
		len(d.VLANsAdd) == 0 && len(d.VLANsUpdate) == 0 && len(d.VLANsDelete) == 0 &&
		len(d.SVIsAdd) == 0 && len(d.SVIsUpdate) == 0 && len(d.SVIsDelete) == 0 &&
		len(d.PortsAdd) == 0 && len(d.PortsUpdate) == 0 &&
		len(d.SubInterfacesAdd) == 0 && len(d.SubInterfacesUpdate) == 0 && len(d.SubInterfacesDelete) == 0 &&
		len(d.BGPPeersAdd) == 0 && len(d.BGPPeersUpdate) == 0 && len(d.BGPPeersDelete) == 0
}

// Count 返回变更总数（不含 unmanaged 报告）。
func (d *Diff) Count() int {
	return len(d.VRFsAdd) + len(d.VRFsUpdate) + len(d.VRFsDelete) +
		len(d.VLANsAdd) + len(d.VLANsUpdate) + len(d.VLANsDelete) +
		len(d.SVIsAdd) + len(d.SVIsUpdate) + len(d.SVIsDelete) +
		len(d.PortsAdd) + len(d.PortsUpdate) +
		len(d.SubInterfacesAdd) + len(d.SubInterfacesUpdate) + len(d.SubInterfacesDelete) +
		len(d.BGPPeersAdd) + len(d.BGPPeersUpdate) + len(d.BGPPeersDelete)
}

// Compute 计算 desired（权威模型）与 actual（设备解析结果）之间的收敛差异。
func Compute(desired, actual *DeviceConfig) *Diff { return compute(desired, actual, true) }

// ComputeVerifiable 与 Compute 相同，但忽略设备无法回读的只写字段（BGP TCP-MD5 密钥）。
// 设备不回显密钥，拿它比对会永远不相等，因此下发后的收敛验证用这个版本。
func ComputeVerifiable(desired, actual *DeviceConfig) *Diff { return compute(desired, actual, false) }

func compute(desired, actual *DeviceConfig, withSecret bool) *Diff {
	d := &Diff{}

	// --- VRF ---
	for name, want := range desired.VRFs {
		got, ok := actual.VRFs[name]
		switch {
		case !ok:
			d.VRFsAdd = append(d.VRFsAdd, *want)
		case !got.Equal(want):
			d.VRFsUpdate = append(d.VRFsUpdate, VRFChange{From: *got, To: *want})
		}
	}
	for name, got := range actual.VRFs {
		if _, ok := desired.VRFs[name]; ok {
			continue
		}
		if got.Managed {
			d.VRFsDelete = append(d.VRFsDelete, *got)
		} else {
			d.UnmanagedVRFs = append(d.UnmanagedVRFs, name)
		}
	}

	// --- VLAN ---
	for id, want := range desired.VLANs {
		got, ok := actual.VLANs[id]
		switch {
		case !ok:
			d.VLANsAdd = append(d.VLANsAdd, *want)
		case !got.Equal(want):
			d.VLANsUpdate = append(d.VLANsUpdate, VLANChange{From: *got, To: *want})
		}
	}
	for id, got := range actual.VLANs {
		if _, ok := desired.VLANs[id]; ok {
			continue
		}
		if got.Managed {
			d.VLANsDelete = append(d.VLANsDelete, *got)
		} else {
			d.UnmanagedVLANs = append(d.UnmanagedVLANs, id)
		}
	}

	// --- SVI ---
	for id, want := range desired.SVIs {
		got, ok := actual.SVIs[id]
		switch {
		case !ok:
			d.SVIsAdd = append(d.SVIsAdd, *want)
		case !got.Equal(want):
			d.SVIsUpdate = append(d.SVIsUpdate, SVIChange{From: *got, To: *want})
		}
	}
	for id, got := range actual.SVIs {
		if _, ok := desired.SVIs[id]; ok {
			continue
		}
		if got.Managed {
			d.SVIsDelete = append(d.SVIsDelete, *got)
		} else {
			d.UnmanagedSVIs = append(d.UnmanagedSVIs, id)
		}
	}

	// --- 物理口 ---
	for name, want := range desired.Ports {
		got, ok := actual.Ports[name]
		switch {
		case !ok:
			d.PortsAdd = append(d.PortsAdd, *want)
		case !got.Equal(want):
			d.PortsUpdate = append(d.PortsUpdate, PortChange{From: *got, To: *want})
		}
	}
	// 端口不支持删除（改模式用 PutPort）：设备上有、期望里没有的端口只报告。
	for name := range actual.Ports {
		if _, ok := desired.Ports[name]; ok {
			continue
		}
		d.UnmanagedPorts = append(d.UnmanagedPorts, name)
	}

	// --- 物理口子接口 ---
	for key, want := range desired.SubInterfaces {
		got, ok := actual.SubInterfaces[key]
		switch {
		case !ok:
			d.SubInterfacesAdd = append(d.SubInterfacesAdd, *want)
		case !got.Equal(want):
			d.SubInterfacesUpdate = append(d.SubInterfacesUpdate, SubInterfaceChange{From: *got, To: *want})
		}
	}
	for key, got := range actual.SubInterfaces {
		if _, ok := desired.SubInterfaces[key]; ok {
			continue
		}
		if got.Managed {
			d.SubInterfacesDelete = append(d.SubInterfacesDelete, *got)
		} else {
			d.UnmanagedSubInterfaces = append(d.UnmanagedSubInterfaces, key)
		}
	}

	// --- BGP 邻居 ---
	for key, want := range desired.BGPPeers {
		got, ok := actual.BGPPeers[key]
		switch {
		case !ok:
			d.BGPPeersAdd = append(d.BGPPeersAdd, *want)
		case !got.equal(want, withSecret):
			d.BGPPeersUpdate = append(d.BGPPeersUpdate, BGPPeerChange{From: *got, To: *want})
		}
	}
	for key, got := range actual.BGPPeers {
		if _, ok := desired.BGPPeers[key]; ok {
			continue
		}
		if got.Managed {
			d.BGPPeersDelete = append(d.BGPPeersDelete, *got)
		} else {
			d.UnmanagedBGPPeers = append(d.UnmanagedBGPPeers, key)
		}
	}
	// 本地 AS 号：设备实际优先（避免手工改过 AS 时再建一个 BGP 进程），其次期望值
	d.BGPASN = actual.BGPASN
	if d.BGPASN == "" {
		d.BGPASN = desired.BGPASN
	}

	// 输出稳定排序（命令顺序可预期，审计可读）
	sort.Slice(d.VRFsAdd, func(i, j int) bool { return d.VRFsAdd[i].Name < d.VRFsAdd[j].Name })
	sort.Slice(d.VRFsUpdate, func(i, j int) bool { return d.VRFsUpdate[i].To.Name < d.VRFsUpdate[j].To.Name })
	sort.Slice(d.VRFsDelete, func(i, j int) bool { return d.VRFsDelete[i].Name < d.VRFsDelete[j].Name })
	sort.Strings(d.UnmanagedVRFs)
	sort.Slice(d.VLANsAdd, func(i, j int) bool { return d.VLANsAdd[i].ID < d.VLANsAdd[j].ID })
	sort.Slice(d.VLANsUpdate, func(i, j int) bool { return d.VLANsUpdate[i].To.ID < d.VLANsUpdate[j].To.ID })
	sort.Slice(d.VLANsDelete, func(i, j int) bool { return d.VLANsDelete[i].ID < d.VLANsDelete[j].ID })
	sort.Ints(d.UnmanagedVLANs)
	sort.Slice(d.SVIsAdd, func(i, j int) bool { return d.SVIsAdd[i].VLANID < d.SVIsAdd[j].VLANID })
	sort.Slice(d.SVIsUpdate, func(i, j int) bool { return d.SVIsUpdate[i].To.VLANID < d.SVIsUpdate[j].To.VLANID })
	sort.Slice(d.SVIsDelete, func(i, j int) bool { return d.SVIsDelete[i].VLANID < d.SVIsDelete[j].VLANID })
	sort.Ints(d.UnmanagedSVIs)
	sort.Slice(d.PortsAdd, func(i, j int) bool { return d.PortsAdd[i].Name < d.PortsAdd[j].Name })
	sort.Slice(d.PortsUpdate, func(i, j int) bool { return d.PortsUpdate[i].To.Name < d.PortsUpdate[j].To.Name })
	sort.Strings(d.UnmanagedPorts)
	sort.Slice(d.SubInterfacesAdd, func(i, j int) bool {
		return SubInterfaceKey(d.SubInterfacesAdd[i].Port, d.SubInterfacesAdd[i].VLAN) <
			SubInterfaceKey(d.SubInterfacesAdd[j].Port, d.SubInterfacesAdd[j].VLAN)
	})
	sort.Slice(d.SubInterfacesUpdate, func(i, j int) bool {
		a, b := d.SubInterfacesUpdate[i].To, d.SubInterfacesUpdate[j].To
		return SubInterfaceKey(a.Port, a.VLAN) < SubInterfaceKey(b.Port, b.VLAN)
	})
	sort.Slice(d.SubInterfacesDelete, func(i, j int) bool {
		return SubInterfaceKey(d.SubInterfacesDelete[i].Port, d.SubInterfacesDelete[i].VLAN) <
			SubInterfaceKey(d.SubInterfacesDelete[j].Port, d.SubInterfacesDelete[j].VLAN)
	})
	sort.Strings(d.UnmanagedSubInterfaces)
	sort.Slice(d.BGPPeersAdd, func(i, j int) bool {
		return PeerKey(d.BGPPeersAdd[i].VRF, d.BGPPeersAdd[i].Neighbor) < PeerKey(d.BGPPeersAdd[j].VRF, d.BGPPeersAdd[j].Neighbor)
	})
	sort.Slice(d.BGPPeersUpdate, func(i, j int) bool {
		a, b := d.BGPPeersUpdate[i].To, d.BGPPeersUpdate[j].To
		return PeerKey(a.VRF, a.Neighbor) < PeerKey(b.VRF, b.Neighbor)
	})
	sort.Slice(d.BGPPeersDelete, func(i, j int) bool {
		return PeerKey(d.BGPPeersDelete[i].VRF, d.BGPPeersDelete[i].Neighbor) < PeerKey(d.BGPPeersDelete[j].VRF, d.BGPPeersDelete[j].Neighbor)
	})
	sort.Strings(d.UnmanagedBGPPeers)

	return d
}
