package model

import (
	"strings"
	"testing"
)

func TestComputeOwnership(t *testing.T) {
	desired := NewDeviceConfig()
	desired.VRFs["CUSTOMER1"] = &VRF{Name: "CUSTOMER1", RD: "65000:1", Managed: true}
	desired.VRFs["CUSTOMER2"] = &VRF{Name: "CUSTOMER2", RD: "65000:2", Managed: true}
	desired.VLANs[10] = &VLAN{ID: 10, Name: "OFFICE", Managed: true}
	desired.SVIs[10] = &SVI{VLANID: 10, IP: "10.0.0.1/24", Managed: true}
	desired.BGPPeers[PeerKey("CUSTOMER1", "10.0.0.2")] = &BGPPeer{
		VRF: "CUSTOMER1", Neighbor: "10.0.0.2", RemoteAS: "65001", Activate: true, Managed: true,
	}

	actual := NewDeviceConfig()
	// 两边都有但字段不同 → update
	actual.VRFs["CUSTOMER1"] = &VRF{Name: "CUSTOMER1", RD: "65000:99", Managed: true}
	// 设备上有标记、期望里没有 → delete
	actual.VRFs["STALE"] = &VRF{Name: "STALE", RD: "65000:9", Managed: true}
	// 设备上无标记、期望里没有 → 仅报告
	actual.VRFs["FOREIGN"] = &VRF{Name: "FOREIGN", RD: "65000:8", Managed: false}
	// 期望里有、设备上没有 → add
	actual.VLANs[20] = &VLAN{ID: 20, Name: "GUEST", Managed: false}

	d := Compute(desired, actual)

	if got := len(d.VRFsAdd); got != 1 || d.VRFsAdd[0].Name != "CUSTOMER2" {
		t.Fatalf("VRFsAdd = %+v", d.VRFsAdd)
	}
	if got := len(d.VRFsUpdate); got != 1 || d.VRFsUpdate[0].To.Name != "CUSTOMER1" ||
		d.VRFsUpdate[0].From.RD == d.VRFsUpdate[0].To.RD {
		t.Fatalf("VRFsUpdate = %+v（应同时带旧值与新值）", d.VRFsUpdate)
	}
	if got := len(d.VRFsDelete); got != 1 || d.VRFsDelete[0].Name != "STALE" {
		t.Fatalf("VRFsDelete = %+v", d.VRFsDelete)
	}
	if len(d.UnmanagedVRFs) != 1 || d.UnmanagedVRFs[0] != "FOREIGN" {
		t.Fatalf("UnmanagedVRFs = %v", d.UnmanagedVRFs)
	}
	if len(d.VLANsAdd) != 1 || len(d.VLANsUpdate) != 0 || len(d.VLANsDelete) != 0 {
		t.Fatalf("VLAN diff = add:%v update:%v delete:%v", d.VLANsAdd, d.VLANsUpdate, d.VLANsDelete)
	}
	if len(d.UnmanagedVLANs) != 1 || d.UnmanagedVLANs[0] != 20 {
		t.Fatalf("UnmanagedVLANs = %v", d.UnmanagedVLANs)
	}
	if len(d.SVIsAdd) != 1 || len(d.BGPPeersAdd) != 1 {
		t.Fatalf("SVI/BGP diff = %+v / %+v", d.SVIsAdd, d.BGPPeersAdd)
	}
	if d.Empty() {
		t.Fatal("diff 不应为空")
	}
	if d.Count() != 6 {
		t.Fatalf("Count = %d, want 6", d.Count())
	}
}

func TestComputeSVIVRFChange(t *testing.T) {
	desired := NewDeviceConfig()
	desired.SVIs[10] = &SVI{VLANID: 10, IP: "10.0.0.1/24", VRF: "CUSTOMER1", Managed: true}
	actual := NewDeviceConfig()
	actual.SVIs[10] = &SVI{VLANID: 10, IP: "10.0.0.1/24", Managed: true}

	d := Compute(desired, actual)
	if len(d.SVIsUpdate) != 1 {
		t.Fatalf("SVIsUpdate = %+v", d.SVIsUpdate)
	}
	if d.SVIsUpdate[0].From.VRF != "" || d.SVIsUpdate[0].To.VRF != "CUSTOMER1" {
		t.Fatalf("SVIChange = %+v", d.SVIsUpdate[0])
	}
}

func TestUpsertAndGuards(t *testing.T) {
	cfg := NewDeviceConfig()
	if err := cfg.UpsertVRF("BAD NAME", "65000:1"); err == nil {
		t.Fatal("非法 VRF 名应被拒绝")
	}
	if err := cfg.UpsertVRF("CUSTOMER1", ""); err == nil {
		t.Fatal("空 RD 应被拒绝")
	}
	if err := cfg.UpsertVRF("CUSTOMER1", "65000:1"); err != nil {
		t.Fatal(err)
	}

	// 平台约束由方言声明、以 Constraints 注入；模型本身不认识厂商。
	caps := Constraints{
		ReservedVLANs:  []IntRange{{From: 1002, To: 1005}},
		VLANNameMaxLen: 28,
	}
	if err := cfg.UpsertVLAN(caps, 1002, "RESERVED"); err == nil {
		t.Fatal("平台保留 VLAN 应被拒绝")
	}
	if err := cfg.UpsertVLAN(caps, 1, ""); err == nil {
		t.Fatal("VLAN 1 应被拒绝")
	}
	if err := cfg.UpsertVLAN(caps, 10, strings.Repeat("N", 29)); err == nil {
		t.Fatal("超过平台名称长度上限应被拒绝")
	}
	if err := cfg.UpsertVLAN(caps, 10, "OFFICE"); err != nil {
		t.Fatal(err)
	}

	if err := cfg.UpsertSVI(20, "10.0.0.1/24"); err == nil {
		t.Fatal("在不存在的 VLAN 上建 SVI 应被拒绝")
	}
	if err := cfg.UpsertSVI(10, "10.0.0.1/255.255.255.0"); err != nil {
		t.Fatal(err)
	}
	if got := cfg.SVIs[10].IP; got != "10.0.0.1/24" {
		t.Fatalf("CIDR 规范化 = %q", got)
	}
	if err := cfg.SetSVIVRF(10, "MISSING"); err == nil {
		t.Fatal("绑定不存在的 VRF 应被拒绝")
	}
	if err := cfg.SetSVIVRF(10, "CUSTOMER1"); err != nil {
		t.Fatal(err)
	}

	if err := cfg.UpsertBGPPeer("CUSTOMER1", "10.0.0.2", "65001"); err != nil {
		t.Fatal(err)
	}
	if err := cfg.DeleteVRF("CUSTOMER1"); err == nil || !strings.Contains(err.Error(), "三层接口") {
		t.Fatalf("删除被 SVI 引用的 VRF 应被拒绝: %v", err)
	}
	if err := cfg.DeleteVLAN(caps, 10); err == nil {
		t.Fatal("删除有 SVI 的 VLAN 应被拒绝")
	}
	if err := cfg.DeleteBGPPeer("CUSTOMER1", "not-an-ip"); err == nil {
		t.Fatal("非法邻居地址应被拒绝")
	}
}

func TestDescribeChanges(t *testing.T) {
	oldCfg := NewDeviceConfig()
	newCfg := NewDeviceConfig()
	if err := newCfg.UpsertVRF("CUSTOMER1", "65000:1"); err != nil {
		t.Fatal(err)
	}
	if err := newCfg.UpsertVLAN(Constraints{}, 10, "OFFICE"); err != nil {
		t.Fatal(err)
	}
	changes := DescribeChanges(oldCfg, newCfg)
	if len(changes) != 2 {
		t.Fatalf("changes = %+v", changes)
	}
	if changes[0].Action != "create" || changes[1].Action != "create" {
		t.Fatalf("changes = %+v", changes)
	}
}
