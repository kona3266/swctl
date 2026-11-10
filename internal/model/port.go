package model

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// PortMode 是物理口的二层/三层模式。
type PortMode string

const (
	// PortModeL2 是二层（switchport）模式。
	PortModeL2 PortMode = "l2"
	// PortModeL3 是三层（no switchport / routed port）模式。
	PortModeL3 PortMode = "l3"
)

func (m PortMode) String() string { return string(m) }

// Port 是被控制器管理的物理口。当前只管理模式（二层/三层），
// 其他端口配置不在管理域内。
type Port struct {
	Name    string   `json:"name"`
	Mode    PortMode `json:"mode"`
	Managed bool     `json:"managed"`
}

// Equal 比较端口字段与标记。
func (p *Port) Equal(o *Port) bool {
	return p.Name == o.Name && p.Mode == o.Mode && p.Managed == o.Managed
}

// SubInterface 是物理口上的一个 VLAN 子接口（802.1Q，如 Ethernet1/1.100）。
// IP 一律以 CIDR 形式存储，厂商的点分掩码由方言层转换。
type SubInterface struct {
	Port string `json:"port"` // 归一化物理口名
	VLAN int    `json:"vlan"` // 802.1Q tag
	IP   string `json:"ip"`   // CIDR
	// VRF 是绑定的 VRF（空 = 全局路由表）
	VRF     string `json:"vrf,omitempty"`
	Managed bool   `json:"managed"`
}

// Equal 比较子接口字段与标记。
func (s *SubInterface) Equal(o *SubInterface) bool {
	return s.Port == o.Port && s.VLAN == o.VLAN && s.IP == o.IP &&
		s.VRF == o.VRF && s.Managed == o.Managed
}

// SubInterfaceKey 返回 SubInterfaces 的 map 键（与设备侧接口名一致，如 Ethernet1/1.100）。
func SubInterfaceKey(port string, vlan int) string {
	return strings.TrimSpace(port) + "." + strconv.Itoa(vlan)
}

// ValidatePortName 校验物理口名。
//
// 只做通用格式校验（字母开头，允许数字 / _ -），点号留给子接口分隔符；
// 平台命名差异（Ethernet1/1、GigabitEthernet0/1…）由方言层决定是否进一步收紧。
func ValidatePortName(name string) error {
	if !portNameRe.MatchString(name) {
		return fmt.Errorf("物理口名 %q 非法：需字母开头，只允许字母/数字/_/-//，长度 1~64"+
			"（如 Ethernet1/1、GigabitEthernet0/1）", name)
	}
	return nil
}

// ParsePortMode 解析端口模式；空串表示“未指定”。
func ParsePortMode(s string) (PortMode, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "l2", "switchport", "二层":
		return PortModeL2, nil
	case "l3", "routed", "三层":
		return PortModeL3, nil
	default:
		return "", fmt.Errorf("端口模式 %q 非法：可选 l2 / l3", s)
	}
}

// UpsertPort 纳管物理口。
//
//   - 新建时 mode 缺省为 l3（本接口的典型用途是承载三层子接口）；
//   - 已存在时 mode 缺省表示“不修改”；
//   - 端口存在子接口时禁止切换模式；
//   - 端口对象不支持删除：恢复二层用 UpsertPort(name, "l2")。
func (c *DeviceConfig) UpsertPort(name, mode string) error {
	name = strings.TrimSpace(name)
	if err := ValidatePortName(name); err != nil {
		return err
	}
	var target PortMode
	if strings.TrimSpace(mode) != "" {
		m, err := ParsePortMode(mode)
		if err != nil {
			return err
		}
		target = m
	}

	cur, ok := c.Ports[name]
	if !ok {
		if target == "" {
			target = PortModeL3
		}
		c.Ports[name] = &Port{Name: name, Mode: target, Managed: true}
		return nil
	}
	if target == "" || target == cur.Mode {
		cur.Managed = true
		return nil
	}
	if c.hasSubInterfaces(name) {
		return fmt.Errorf("端口 %s 存在子接口，不能从 %s 切换为 %s："+
			"请先删除其全部子接口", name, cur.Mode, target)
	}
	cur.Mode = target
	return nil
}

// UpsertSubInterface 创建或更新物理口上的 VLAN 子接口。
//
//   - 父物理口必须已被纳管且为三层模式；
//   - IP 必填（CIDR）；
//   - vrf 为 nil 表示不修改；指向空串表示移回全局路由表。
func (c *DeviceConfig) UpsertSubInterface(port string, vlan int, ip string, vrf *string) error {
	port = strings.TrimSpace(port)
	if err := ValidatePortName(port); err != nil {
		return err
	}
	if vlan < 1 || vlan > 4094 {
		return fmt.Errorf("子接口的 VLAN ID %d 非法：802.1Q tag 有效范围 1~4094", vlan)
	}
	normalized, err := NormalizeCIDR(ip)
	if err != nil {
		return err
	}
	p, ok := c.Ports[port]
	if !ok {
		return fmt.Errorf("物理口 %s 不在管理中：请先纳管该端口（PUT /ports/%s）", port, port)
	}
	if p.Mode != PortModeL3 {
		return fmt.Errorf("物理口 %s 当前为二层模式：子接口只能建在三层口上", port)
	}

	key := SubInterfaceKey(port, vlan)
	si := &SubInterface{Port: port, VLAN: vlan, IP: normalized, Managed: true}
	if cur, ok := c.SubInterfaces[key]; ok {
		cp := *cur
		si = &cp
		si.IP = normalized
	}
	if vrf != nil {
		v := strings.TrimSpace(*vrf)
		if v == "" {
			si.VRF = ""
		} else {
			if _, ok := c.VRFs[v]; !ok {
				return fmt.Errorf("VRF %s 不在管理中：请先创建或纳管该 VRF", v)
			}
			si.VRF = v
		}
	}
	c.SubInterfaces[key] = si
	return nil
}

// SetSubInterfaceVRF 把子接口加入 VRF（要求子接口与 VRF 都在管理中）。
func (c *DeviceConfig) SetSubInterfaceVRF(port string, vlan int, vrf string) error {
	key, err := validateSubInterfaceKey(port, vlan)
	if err != nil {
		return err
	}
	si, ok := c.SubInterfaces[key]
	if !ok {
		return fmt.Errorf("子接口 %s 不在管理中：请先创建该子接口", key)
	}
	vrf = strings.TrimSpace(vrf)
	if vrf == "" {
		return fmt.Errorf("VRF 不能为空：如需移出 VRF 请使用 DELETE .../vrf")
	}
	if _, ok := c.VRFs[vrf]; !ok {
		return fmt.Errorf("VRF %s 不在管理中：请先创建或纳管该 VRF", vrf)
	}
	si.VRF = vrf
	return nil
}

// ClearSubInterfaceVRF 把子接口移出 VRF（未绑定时为幂等空操作）。
func (c *DeviceConfig) ClearSubInterfaceVRF(port string, vlan int) error {
	key, err := validateSubInterfaceKey(port, vlan)
	if err != nil {
		return err
	}
	si, ok := c.SubInterfaces[key]
	if !ok {
		return fmt.Errorf("子接口 %s 不在管理中：请先创建该子接口", key)
	}
	si.VRF = ""
	return nil
}

// DeleteSubInterface 从期望模型中移除子接口。
func (c *DeviceConfig) DeleteSubInterface(port string, vlan int) error {
	key, err := validateSubInterfaceKey(port, vlan)
	if err != nil {
		return err
	}
	delete(c.SubInterfaces, key)
	return nil
}

// validateSubInterfaceKey 校验子接口标识并返回 map 键。
func validateSubInterfaceKey(port string, vlan int) (string, error) {
	port = strings.TrimSpace(port)
	if err := ValidatePortName(port); err != nil {
		return "", err
	}
	if vlan < 1 || vlan > 4094 {
		return "", fmt.Errorf("子接口的 VLAN ID %d 非法：802.1Q tag 有效范围 1~4094", vlan)
	}
	return SubInterfaceKey(port, vlan), nil
}

func (c *DeviceConfig) hasSubInterfaces(port string) bool {
	for _, s := range c.SubInterfaces {
		if s.Port == port {
			return true
		}
	}
	return false
}

var portNameRe = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9/_-]{0,63}$`)
