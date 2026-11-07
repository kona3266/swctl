package model

import (
	"fmt"
	"regexp"
)

// VLAN 是一个 VLAN 对象。
type VLAN struct {
	ID   int    `json:"id"`
	Name string `json:"name,omitempty"`
	// Managed 同上
	Managed bool `json:"managed"`
}

// Equal 比较两个 VLAN 的业务字段与标记。
func (v *VLAN) Equal(o *VLAN) bool {
	return v.ID == o.ID && v.Name == o.Name && v.Managed == o.Managed
}

// ValidateVLAN 校验 VLAN 参数。平台相关的约束（保留 VLAN 区间、名称长度上限）
// 通过 c 注入，模型本身不认识具体厂商。
func ValidateVLAN(c Constraints, id int, name string) error {
	if id < 1 || id > 4094 {
		return fmt.Errorf("VLAN ID %d 非法：有效范围 1~4094", id)
	}
	if id == 1 {
		return fmt.Errorf("VLAN 1 为默认 VLAN，禁止通过业务接口修改或删除")
	}
	if r, ok := c.ReservedVLAN(id); ok {
		return fmt.Errorf("VLAN %d 属于平台保留区间（%d~%d），禁止操作", id, r.From, r.To)
	}
	if name != "" && !vlanNameRe.MatchString(name) {
		return fmt.Errorf("VLAN 名称 %q 非法：只允许字母/数字/_.-", name)
	}
	if name != "" && c.VLANNameMaxLen > 0 && len(name) > c.VLANNameMaxLen {
		return fmt.Errorf("VLAN 名称过长：%d 个字符超过平台上限 %d", len(name), c.VLANNameMaxLen)
	}
	return nil
}

// UpsertVLAN 创建或更新 VLAN。name 为业务名（不含管理标记后缀）。
func (c *DeviceConfig) UpsertVLAN(cs Constraints, id int, name string) error {
	if err := ValidateVLAN(cs, id, name); err != nil {
		return err
	}
	// 不提供 name 时保留已有名称（merge 语义）
	if name == "" {
		if old, ok := c.VLANs[id]; ok {
			name = old.Name
		}
	}
	c.VLANs[id] = &VLAN{ID: id, Name: name, Managed: true}
	return nil
}

// DeleteVLAN 从期望模型中移除 VLAN。该 VLAN 上还有 SVI 时拒绝（会留下悬空接口）。
func (c *DeviceConfig) DeleteVLAN(cs Constraints, id int) error {
	if id < 1 || id > 4094 || id == 1 {
		return fmt.Errorf("VLAN ID %d 非法：有效范围 2~4094", id)
	}
	if r, ok := cs.ReservedVLAN(id); ok {
		return fmt.Errorf("VLAN %d 属于平台保留区间（%d~%d），禁止操作", id, r.From, r.To)
	}
	if _, ok := c.SVIs[id]; ok {
		return fmt.Errorf("VLAN %d 上存在三层接口（SVI），请先删除该接口再删除 VLAN", id)
	}
	delete(c.VLANs, id)
	return nil
}

// 业务参数校验。
var (
	vlanNameRe = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)
)
