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

// Empty 表示无需任何变更。
func (d *Diff) Empty() bool {
	return len(d.VRFsAdd) == 0 && len(d.VRFsUpdate) == 0 && len(d.VRFsDelete) == 0 &&
		len(d.VLANsAdd) == 0 && len(d.VLANsUpdate) == 0 && len(d.VLANsDelete) == 0 &&
		len(d.SVIsAdd) == 0 && len(d.SVIsUpdate) == 0 && len(d.SVIsDelete) == 0 &&
		len(d.BGPPeersAdd) == 0 && len(d.BGPPeersUpdate) == 0 && len(d.BGPPeersDelete) == 0
}

// Count 返回变更总数（不含 unmanaged 报告）。
func (d *Diff) Count() int {
	return len(d.VRFsAdd) + len(d.VRFsUpdate) + len(d.VRFsDelete) +
		len(d.VLANsAdd) + len(d.VLANsUpdate) + len(d.VLANsDelete) +
		len(d.SVIsAdd) + len(d.SVIsUpdate) + len(d.SVIsDelete) +
		len(d.BGPPeersAdd) + len(d.BGPPeersUpdate) + len(d.BGPPeersDelete)
}

// Compute 计算 desired（权威模型）与 actual（设备解析结果）之间的收敛差异。
func Compute(desired, actual *DeviceConfig) *Diff {
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

	// --- BGP 邻居 ---
	for key, want := range desired.BGPPeers {
		got, ok := actual.BGPPeers[key]
		switch {
		case !ok:
			d.BGPPeersAdd = append(d.BGPPeersAdd, *want)
		case !got.Equal(want):
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
