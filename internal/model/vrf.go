package model

import (
	"fmt"
	"regexp"
)

// VRF 是一个 VRF 对象。
type VRF struct {
	Name string `json:"name"`
	RD   string `json:"rd,omitempty"`
	// Managed 表示设备侧存在我们的管理标记（解析自设备配置；权威模型里恒为 true）
	Managed bool `json:"managed"`
}

// Equal 比较两个 VRF 的业务字段与标记。
func (v *VRF) Equal(o *VRF) bool {
	return v.Name == o.Name && v.RD == o.RD && v.Managed == o.Managed
}

// ValidateVRFName 校验 VRF 名称。
func ValidateVRFName(name string) error {
	if !vrfNameRe.MatchString(name) {
		return fmt.Errorf("VRF 名称 %q 非法：只允许字母/数字/_.-，长度 1~32", name)
	}
	return nil
}

// ValidateRD 校验 RD 格式（必填项，空值在这里也视为非法）。
func ValidateRD(rd string) error {
	if rd == "" {
		return fmt.Errorf("RD 不能为空：格式应为 <AS号>:<编号> 或 <IP>:<编号>，如 65000:1")
	}
	if !rdRe.MatchString(rd) {
		return fmt.Errorf("RD %q 非法：格式应为 <AS号>:<编号> 或 <IP>:<编号>，如 65000:1", rd)
	}
	return nil
}

// UpsertVRF 创建或更新 VRF（模型层面 upsert；权威模型中的对象恒为 Managed）。
func (c *DeviceConfig) UpsertVRF(name, rd string) error {
	if err := ValidateVRFName(name); err != nil {
		return err
	}
	if err := ValidateRD(rd); err != nil {
		return err
	}
	c.VRFs[name] = &VRF{Name: name, RD: rd, Managed: true}
	return nil
}

// DeleteVRF 从期望模型中移除 VRF（若不存在则为幂等空操作）。
// 仍被管理的 SVI 或 BGP 邻居引用时拒绝（先解绑/删邻居，避免悬空配置）。
func (c *DeviceConfig) DeleteVRF(name string) error {
	if err := ValidateVRFName(name); err != nil {
		return err
	}
	for _, s := range c.SVIs {
		if s.VRF == name {
			return fmt.Errorf("VRF %s 仍被三层接口 Vlan%d 引用：请先将其移出该 VRF", name, s.VLANID)
		}
	}
	for _, p := range c.BGPPeers {
		if p.VRF == name {
			return fmt.Errorf("VRF %s 仍配置有 BGP 邻居 %s：请先删除邻居", name, p.Neighbor)
		}
	}
	delete(c.VRFs, name)
	return nil
}

// 业务参数校验。
var (
	vrfNameRe = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,32}$`)
	rdRe      = regexp.MustCompile(`^(\d+|\d{1,3}(\.\d{1,3}){3}):\d{1,10}$`)
)
