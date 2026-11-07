// Package dialect 是平台能力与后端实现的唯一注册表（per-driver 登记点）。
//
// 业务层只通过这里获取两样东西：
//   - Constraints：平台对业务对象的约束（保留 VLAN 区间、名称长度上限…），
//     用于在提交期望态前做校验；
//   - NewDevice：构造该平台的 southbound 后端（NETCONF / NX-API / …）。
//
// 平台语法、标记编码、配置归一化都封装在后端内部，不对外暴露。
//
// 当前注册表为空：SSH CLI 后端已移除，模型驱动后端待接入
// （cisco_nxos → NX-API；ruijie_rgos → NETCONF）。
package dialect

import (
	"switchctl/internal/device/driver"
	"switchctl/internal/model"
)

// Impl 是某平台的全部对外能力。
type Impl struct {
	Name string
	// Constraints 是平台对业务对象的约束。
	Constraints model.Constraints
	// AtomicApply 表示该平台的 Apply 能保证"整批差异在一次原子提交内生效"。
	// 控制器不做失败回滚，因此后端的原子性是前提；未声明时会在操作记录里给出告警。
	AtomicApply bool
	// NewDevice 构造该平台的 southbound 后端。
	NewDevice func(driver.Config) (driver.Device, error)
}

// Get 按平台名返回实现。
func Get(name string) (Impl, bool) {
	switch name {
	default:
		return Impl{}, false
	}
}
