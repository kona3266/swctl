package model

import (
	"encoding/json"
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

	if err := cfg.UpsertBGPPeer("CUSTOMER1", "10.0.0.2", BGPPeerParams{RemoteAS: strPtr("65001")}); err != nil {
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

func strPtr(s string) *string { return &s }
func boolPtr(b bool) *bool    { return &b }
func intPtr(i int) *int       { return &i }

func TestUpsertBGPPeerParams(t *testing.T) {
	cfg := NewDeviceConfig()
	cfg.VRFs["CUSTOMER1"] = &VRF{Name: "CUSTOMER1", RD: "65000:1", Managed: true}
	cfg.BGPASN = "65000"
	key := PeerKey("CUSTOMER1", "10.0.0.2")

	if err := cfg.UpsertBGPPeer("CUSTOMER1", "10.0.0.2", BGPPeerParams{
		RemoteAS:             strPtr("65000"),
		SourceIP:             strPtr("10.255.0.1"),
		Password:             strPtr("s3cret"),
		BFD:                  boolPtr(true),
		BFDMinRxMS:           intPtr(300),
		BFDMinTxMS:           intPtr(300),
		BFDMultiplier:        intPtr(3),
		MaxPrefix:            intPtr(5000),
		MaxPrefixWarningOnly: boolPtr(true),
		RouteReflectorClient: boolPtr(true),
	}); err != nil {
		t.Fatal(err)
	}
	p := cfg.BGPPeers[key]
	if p.SourceIP != "10.255.0.1" || p.Password != "s3cret" || !p.BFD || p.BFDMinRxMS != 300 || p.BFDMinTxMS != 300 ||
		p.BFDMultiplier != 3 || p.MaxPrefix != 5000 || !p.MaxPrefixWarningOnly ||
		!p.RouteReflectorClient || !p.Activate || !p.Managed {
		t.Fatalf("peer = %+v", p)
	}

	// 局部更新：只给 remote_as，不应清掉其它参数
	if err := cfg.UpsertBGPPeer("CUSTOMER1", "10.0.0.2", BGPPeerParams{RemoteAS: strPtr("65000")}); err != nil {
		t.Fatal(err)
	}
	if p = cfg.BGPPeers[key]; p.SourceIP != "10.255.0.1" || p.Password != "s3cret" || p.MaxPrefix != 5000 || !p.RouteReflectorClient {
		t.Fatalf("局部更新清掉了未给出的参数: %+v", p)
	}

	// 显式清空
	if err := cfg.UpsertBGPPeer("CUSTOMER1", "10.0.0.2", BGPPeerParams{
		SourceIP:             strPtr(""),
		Password:             strPtr(""),
		BFD:                  boolPtr(false),
		BFDMinRxMS:           intPtr(0),
		BFDMinTxMS:           intPtr(0),
		BFDMultiplier:        intPtr(0),
		MaxPrefix:            intPtr(0),
		MaxPrefixWarningOnly: boolPtr(false),
		RouteReflectorClient: boolPtr(false),
	}); err != nil {
		t.Fatal(err)
	}
	if p = cfg.BGPPeers[key]; p.SourceIP != "" || p.Password != "" || p.BFD || p.MaxPrefix != 0 || p.MaxPrefixWarningOnly {
		t.Fatalf("显式清空未生效: %+v", p)
	}

	// route_reflector_client 只能用于 iBGP；失败时不污染模型
	err := cfg.UpsertBGPPeer("CUSTOMER1", "10.0.0.2", BGPPeerParams{
		RemoteAS: strPtr("65001"), RouteReflectorClient: boolPtr(true),
	})
	if err == nil || !strings.Contains(err.Error(), "iBGP") {
		t.Fatalf("eBGP 邻居设置 RR client 应被拒绝: %v", err)
	}
	if p = cfg.BGPPeers[key]; p.RemoteAS != "65000" || p.RouteReflectorClient {
		t.Fatalf("校验失败污染了权威模型: %+v", p)
	}
}

func TestBGPPeerValidation(t *testing.T) {
	newCfg := func() *DeviceConfig {
		cfg := NewDeviceConfig()
		cfg.VRFs["CUSTOMER1"] = &VRF{Name: "CUSTOMER1", RD: "65000:1", Managed: true}
		cfg.BGPASN = "65000"
		return cfg
	}
	cases := []struct {
		name   string
		mutate func(*BGPPeerParams)
		want   string
	}{
		{"source_ip 为 IPv6", func(p *BGPPeerParams) { p.SourceIP = strPtr("2001:db8::1") }, "source_ip"},
		{"source_ip 为 0.0.0.0", func(p *BGPPeerParams) { p.SourceIP = strPtr("0.0.0.0") }, "source_ip"},
		{"source_ip 为 127.0.0.1", func(p *BGPPeerParams) { p.SourceIP = strPtr("127.0.0.1") }, "source_ip"},
		{"source_ip 非法", func(p *BGPPeerParams) { p.SourceIP = strPtr("10.0.0.1/24") }, "source_ip"},
		{"密码含空格", func(p *BGPPeerParams) { p.Password = strPtr("bad pass") }, "ASCII"},
		{"密码过长", func(p *BGPPeerParams) { p.Password = strPtr(strings.Repeat("a", MaxBGPSecretLen+1)) }, "过长"},
		{"定时器未开 BFD", func(p *BGPPeerParams) { p.BFDMinRxMS = intPtr(300) }, "bfd"},
		{"定时器越界", func(p *BGPPeerParams) { p.BFD = boolPtr(true); p.BFDMinTxMS = intPtr(10) }, "非法"},
		{"multiplier 越界", func(p *BGPPeerParams) { p.BFD = boolPtr(true); p.BFDMultiplier = intPtr(51) }, "bfd_multiplier"},
		{"max_prefix 为负", func(p *BGPPeerParams) { p.MaxPrefix = intPtr(-1) }, "max_prefix"},
		{"warning_only 缺 max_prefix", func(p *BGPPeerParams) { p.MaxPrefixWarningOnly = boolPtr(true) }, "max_prefix"},
		{"缺 remote_as", func(p *BGPPeerParams) { p.RemoteAS = nil }, "remote_as"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			params := BGPPeerParams{RemoteAS: strPtr("65001")}
			tc.mutate(&params)
			if err := newCfg().UpsertBGPPeer("CUSTOMER1", "10.0.0.2", params); err == nil ||
				!strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v，应包含 %q", err, tc.want)
			}
		})
	}
}

func TestComputeVerifiableIgnoresPassword(t *testing.T) {
	key := PeerKey("CUSTOMER1", "10.0.0.2")
	desired := NewDeviceConfig()
	desired.BGPPeers[key] = &BGPPeer{
		VRF: "CUSTOMER1", Neighbor: "10.0.0.2", RemoteAS: "65001",
		Password: "s3cret", BFD: true, MaxPrefix: 100, Activate: true, Managed: true,
	}
	// 设备不回显密码
	actual := NewDeviceConfig()
	actual.BGPPeers[key] = &BGPPeer{
		VRF: "CUSTOMER1", Neighbor: "10.0.0.2", RemoteAS: "65001",
		BFD: true, MaxPrefix: 100, Activate: true, Managed: true,
	}

	if d := Compute(desired, actual); len(d.BGPPeersUpdate) != 1 {
		t.Fatalf("下发差异应包含密码变化: %+v", d.BGPPeersUpdate)
	}
	if d := ComputeVerifiable(desired, actual); !d.Empty() {
		t.Fatalf("验证差异应忽略密码: %+v", d.BGPPeersUpdate)
	}
}

func TestBGPSecretMasking(t *testing.T) {
	key := PeerKey("CUSTOMER1", "10.0.0.2")
	old := NewDeviceConfig()
	old.BGPPeers[key] = &BGPPeer{VRF: "CUSTOMER1", Neighbor: "10.0.0.2", RemoteAS: "65001", Managed: true, Activate: true}
	cur := NewDeviceConfig()
	cur.BGPPeers[key] = &BGPPeer{
		VRF: "CUSTOMER1", Neighbor: "10.0.0.2", RemoteAS: "65001",
		Password: "s3cret", Managed: true, Activate: true,
	}

	var found bool
	for _, c := range DescribeChanges(old, cur) {
		for _, f := range c.Fields {
			if f.Field != "password" {
				continue
			}
			found = true
			if f.From != "" || f.To != MaskedSecret {
				t.Fatalf("变更摘要未脱敏: %+v", f)
			}
		}
	}
	if !found {
		t.Fatal("变更摘要缺少 password 字段")
	}

	d := Compute(cur, old)
	sd := d.Sanitized()
	if len(sd.BGPPeersUpdate) != 1 || sd.BGPPeersUpdate[0].To.Password != MaskedSecret {
		t.Fatalf("脱敏后的 delta = %+v", sd.BGPPeersUpdate)
	}
	if d.BGPPeersUpdate[0].To.Password != "s3cret" {
		t.Fatal("Sanitized 不应修改原 diff（下发仍需真实值）")
	}
	raw, err := json.Marshal(sd)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "s3cret") {
		t.Fatalf("delta JSON 泄露明文密码: %s", raw)
	}
}
