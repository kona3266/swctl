package model

import (
	"strings"
	"testing"
)

func newPortCfg(t *testing.T) *DeviceConfig {
	t.Helper()
	cfg := NewDeviceConfig()
	if err := cfg.UpsertVRF("CUSTOMER1", "65000:1"); err != nil {
		t.Fatal(err)
	}
	return cfg
}

func TestPortModeGuards(t *testing.T) {
	cfg := newPortCfg(t)

	// 缺省建为三层口
	if err := cfg.UpsertPort("Ethernet1/1", ""); err != nil {
		t.Fatal(err)
	}
	if got := cfg.Ports["Ethernet1/1"].Mode; got != PortModeL3 {
		t.Fatalf("默认模式 = %s，应为 l3", got)
	}
	if err := cfg.UpsertPort("Ethernet1/1", "l2"); err != nil {
		t.Fatalf("无子接口时应允许切换: %v", err)
	}
	if err := cfg.UpsertPort("Ethernet1/1", "l3"); err != nil {
		t.Fatal(err)
	}

	if err := cfg.UpsertSubInterface("Ethernet1/1", 100, "10.0.0.1/24", nil); err != nil {
		t.Fatal(err)
	}
	if err := cfg.UpsertPort("Ethernet1/1", "l2"); err == nil ||
		!strings.Contains(err.Error(), "子接口") {
		t.Fatalf("有子接口时切换模式应被拒绝: %v", err)
	}
	if err := cfg.DeleteSubInterface("Ethernet1/1", 100); err != nil {
		t.Fatal(err)
	}
	// 恢复二层用 PUT mode=l2（没有删除端口的操作）
	if err := cfg.UpsertPort("Ethernet1/1", "l2"); err != nil {
		t.Fatalf("子接口删完后应允许切换: %v", err)
	}
	if got := cfg.Ports["Ethernet1/1"].Mode; got != PortModeL2 {
		t.Fatalf("模式 = %s，应为 l2", got)
	}
}

func TestUpsertSubInterfaceGuards(t *testing.T) {
	cfg := newPortCfg(t)

	if err := cfg.UpsertSubInterface("Ethernet1/1", 100, "10.0.0.1/24", nil); err == nil ||
		!strings.Contains(err.Error(), "不在管理中") {
		t.Fatalf("未纳管端口应被拒绝: %v", err)
	}

	if err := cfg.UpsertPort("Ethernet1/2", "l2"); err != nil {
		t.Fatal(err)
	}
	if err := cfg.UpsertSubInterface("Ethernet1/2", 100, "10.0.0.1/24", nil); err == nil ||
		!strings.Contains(err.Error(), "二层模式") {
		t.Fatalf("二层口上建子接口应被拒绝: %v", err)
	}

	if err := cfg.UpsertPort("Ethernet1/1", "l3"); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name string
		vlan int
		ip   string
		want string
	}{
		{"VLAN 0", 0, "10.0.0.1/24", "VLAN ID"},
		{"VLAN 4095", 4095, "10.0.0.1/24", "VLAN ID"},
		{"IP 为空", 100, "", "IP 地址不能为空"},
		{"IP 非法", 100, "10.0.0.1", "掩码"},
	}
	for _, tc := range cases {
		if err := cfg.UpsertSubInterface("Ethernet1/1", tc.vlan, tc.ip, nil); err == nil ||
			!strings.Contains(err.Error(), tc.want) {
			t.Fatalf("%s: err = %v，应包含 %q", tc.name, err, tc.want)
		}
	}

	missing := "NOPE"
	if err := cfg.UpsertSubInterface("Ethernet1/1", 100, "10.0.0.1/24", &missing); err == nil ||
		!strings.Contains(err.Error(), "不在管理中") {
		t.Fatalf("绑定未管理 VRF 应被拒绝: %v", err)
	}
}

func TestSubInterfacePartialUpdate(t *testing.T) {
	cfg := newPortCfg(t)
	if err := cfg.UpsertPort("Ethernet1/1", "l3"); err != nil {
		t.Fatal(err)
	}
	// 点分掩码写法应被规范化为 CIDR
	if err := cfg.UpsertSubInterface("Ethernet1/1", 100, "10.0.0.1/255.255.255.0", nil); err != nil {
		t.Fatal(err)
	}
	key := SubInterfaceKey("Ethernet1/1", 100)
	if got := cfg.SubInterfaces[key].IP; got != "10.0.0.1/24" {
		t.Fatalf("CIDR 规范化 = %q", got)
	}

	vrf := "CUSTOMER1"
	if err := cfg.UpsertSubInterface("Ethernet1/1", 100, "10.0.0.1/24", &vrf); err != nil {
		t.Fatal(err)
	}
	// 不传 vrf 时保持绑定
	if err := cfg.UpsertSubInterface("Ethernet1/1", 100, "10.0.0.2/24", nil); err != nil {
		t.Fatal(err)
	}
	if got := cfg.SubInterfaces[key]; got.VRF != "CUSTOMER1" || got.IP != "10.0.0.2/24" {
		t.Fatalf("局部更新结果 = %+v", got)
	}
	// 显式空串移回全局表
	empty := ""
	if err := cfg.UpsertSubInterface("Ethernet1/1", 100, "10.0.0.2/24", &empty); err != nil {
		t.Fatal(err)
	}
	if got := cfg.SubInterfaces[key].VRF; got != "" {
		t.Fatalf("VRF 应为空，实际 %q", got)
	}

	// VRF 被子接口引用时禁止删除
	if err := cfg.UpsertSubInterface("Ethernet1/1", 100, "10.0.0.2/24", &vrf); err != nil {
		t.Fatal(err)
	}
	if err := cfg.DeleteVRF("CUSTOMER1"); err == nil || !strings.Contains(err.Error(), "子接口") {
		t.Fatalf("被引用时删除 VRF 应被拒绝: %v", err)
	}
}

func TestComputePortsAndSubInterfaces(t *testing.T) {
	desired := NewDeviceConfig()
	desired.Ports["Ethernet1/1"] = &Port{Name: "Ethernet1/1", Mode: PortModeL3, Managed: true}
	desired.SubInterfaces[SubInterfaceKey("Ethernet1/1", 100)] = &SubInterface{
		Port: "Ethernet1/1", VLAN: 100, IP: "10.0.0.1/24", Managed: true,
	}

	actual := NewDeviceConfig()
	// 模式不同 → update
	actual.Ports["Ethernet1/1"] = &Port{Name: "Ethernet1/1", Mode: PortModeL2, Managed: true}
	// 设备上有、期望里没有 → 只报告（端口不删除）
	actual.Ports["Ethernet1/2"] = &Port{Name: "Ethernet1/2", Mode: PortModeL3, Managed: true}
	// 无标记 → 同样只报告
	actual.Ports["Ethernet1/3"] = &Port{Name: "Ethernet1/3", Mode: PortModeL2, Managed: false}
	// 子接口：期望里没有、带标记 → delete
	actual.SubInterfaces[SubInterfaceKey("Ethernet1/2", 200)] = &SubInterface{
		Port: "Ethernet1/2", VLAN: 200, IP: "10.1.0.1/24", Managed: true,
	}
	// 子接口：无标记 → unmanaged
	actual.SubInterfaces[SubInterfaceKey("Ethernet1/3", 300)] = &SubInterface{
		Port: "Ethernet1/3", VLAN: 300, IP: "10.2.0.1/24", Managed: false,
	}

	d := Compute(desired, actual)
	if len(d.PortsAdd) != 0 || len(d.PortsUpdate) != 1 {
		t.Fatalf("port diff = add:%v update:%v", d.PortsAdd, d.PortsUpdate)
	}
	// 未在期望态里的端口（无论有无标记）都只报告，不删除
	if len(d.UnmanagedPorts) != 2 || d.UnmanagedPorts[0] != "Ethernet1/2" || d.UnmanagedPorts[1] != "Ethernet1/3" {
		t.Fatalf("UnmanagedPorts = %v", d.UnmanagedPorts)
	}
	if d.PortsUpdate[0].From.Mode != PortModeL2 || d.PortsUpdate[0].To.Mode != PortModeL3 {
		t.Fatalf("PortChange = %+v", d.PortsUpdate[0])
	}
	if len(d.SubInterfacesAdd) != 1 || len(d.SubInterfacesDelete) != 1 {
		t.Fatalf("sub-interface diff = add:%v delete:%v", d.SubInterfacesAdd, d.SubInterfacesDelete)
	}
	if len(d.UnmanagedSubInterfaces) != 1 ||
		d.UnmanagedSubInterfaces[0] != SubInterfaceKey("Ethernet1/3", 300) {
		t.Fatalf("UnmanagedSubInterfaces = %v", d.UnmanagedSubInterfaces)
	}
	if d.Count() != 3 {
		t.Fatalf("Count = %d, want 3", d.Count())
	}
}

func TestDescribeChangesPortsAndSubInterfaces(t *testing.T) {
	old := NewDeviceConfig()
	old.Ports["Ethernet1/1"] = &Port{Name: "Ethernet1/1", Mode: PortModeL3, Managed: true}
	old.SubInterfaces[SubInterfaceKey("Ethernet1/1", 100)] = &SubInterface{
		Port: "Ethernet1/1", VLAN: 100, IP: "10.0.0.1/24", Managed: true,
	}

	cur := old.Clone()
	cur.Ports["Ethernet1/1"].Mode = PortModeL2
	cur.SubInterfaces[SubInterfaceKey("Ethernet1/1", 100)].IP = "10.0.0.2/24"
	cur.SubInterfaces[SubInterfaceKey("Ethernet1/1", 100)].VRF = "CUSTOMER1"

	got := DescribeChanges(old, cur)
	if len(got) != 2 {
		t.Fatalf("changes = %+v", got)
	}
	byResource := map[string]ObjectChange{}
	for _, c := range got {
		byResource[c.Resource] = c
	}
	port, ok := byResource["port"]
	if !ok || port.Action != "update" || port.Fields[0].Field != "mode" ||
		port.Fields[0].From != "l3" || port.Fields[0].To != "l2" {
		t.Fatalf("port change = %+v", port)
	}
	sub, ok := byResource["sub-interface"]
	if !ok || sub.Action != "update" || len(sub.Fields) != 2 {
		t.Fatalf("sub-interface change = %+v", sub)
	}
}

func TestSubInterfaceVRFBinding(t *testing.T) {
	cfg := newPortCfg(t) // 已建 CUSTOMER1
	if err := cfg.UpsertPort("Ethernet1/1", "l3"); err != nil {
		t.Fatal(err)
	}

	// 子接口不存在 → 拒绝
	if err := cfg.SetSubInterfaceVRF("Ethernet1/1", 100, "CUSTOMER1"); err == nil ||
		!strings.Contains(err.Error(), "不在管理中") {
		t.Fatalf("绑定不存在的子接口应被拒绝: %v", err)
	}

	if err := cfg.UpsertSubInterface("Ethernet1/1", 100, "10.0.0.1/24", nil); err != nil {
		t.Fatal(err)
	}
	// VRF 不存在 → 拒绝
	if err := cfg.SetSubInterfaceVRF("Ethernet1/1", 100, "NOPE"); err == nil ||
		!strings.Contains(err.Error(), "不在管理中") {
		t.Fatalf("绑定不存在的 VRF 应被拒绝: %v", err)
	}
	// 空 VRF → 拒绝（移出应用 DELETE /vrf）
	if err := cfg.SetSubInterfaceVRF("Ethernet1/1", 100, ""); err == nil {
		t.Fatal("空 VRF 应被拒绝")
	}
	// VLAN 越界 → 拒绝
	if err := cfg.SetSubInterfaceVRF("Ethernet1/1", 4095, "CUSTOMER1"); err == nil {
		t.Fatal("VLAN 越界应被拒绝")
	}

	if err := cfg.SetSubInterfaceVRF("Ethernet1/1", 100, "CUSTOMER1"); err != nil {
		t.Fatal(err)
	}
	key := SubInterfaceKey("Ethernet1/1", 100)
	if got := cfg.SubInterfaces[key].VRF; got != "CUSTOMER1" {
		t.Fatalf("VRF = %q", got)
	}
	// 重复绑定幂等
	if err := cfg.SetSubInterfaceVRF("Ethernet1/1", 100, "CUSTOMER1"); err != nil {
		t.Fatal(err)
	}

	// 移出 VRF（重复调用幂等）
	if err := cfg.ClearSubInterfaceVRF("Ethernet1/1", 100); err != nil {
		t.Fatal(err)
	}
	if err := cfg.ClearSubInterfaceVRF("Ethernet1/1", 100); err != nil {
		t.Fatal(err)
	}
	if got := cfg.SubInterfaces[key].VRF; got != "" {
		t.Fatalf("移出后 VRF = %q", got)
	}
	if err := cfg.ClearSubInterfaceVRF("Ethernet1/1", 999); err == nil {
		t.Fatal("对不存在的子接口解绑应被拒绝")
	}
}
