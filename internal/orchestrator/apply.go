package orchestrator

import (
	"encoding/json"
	"fmt"

	"switchctl/internal/model"
	"switchctl/internal/store"
)

// apply 处理一条业务变更操作。
//
// 与早期实现的区别：**不推送提交时算好的模型 delta，而是拉取设备实际状态后重新算 diff**。
// 原因：delta 隐含"设备此刻等于提交时的期望态"的假设，一旦设备漂移（手工改动、
// 上一次收敛半途失败、刚发生过回滚），按旧基线算出的 update/delete 打到设备上会出错。
// 改成 pull → diff 后，apply 与 reconcile 走同一套收敛语义，天然幂等、可重试。
func (run *opRun) apply() {
	defer func() {
		if run.conn != nil {
			_ = run.conn.Close()
		}
	}()

	// 1. 目标模型：本操作应把设备收敛到的版本（= 提交时记录的 base_version + 1）。
	//    取不到历史版本时退回当前期望态快照。
	target, targetVersion, err := run.applyTarget()
	if err != nil {
		run.finish(store.StatusFailed, "target_unavailable", err.Error(), true)
		return
	}

	if err := run.connect(); err != nil {
		run.finish(store.StatusFailed, "device_unreachable", err.Error(), true)
		return
	}

	// 2. 拉取设备实际状态
	actual, err := run.pullConfig()
	if err != nil {
		run.finish(store.StatusFailed, "apply_pull_failed", err.Error(), true)
		return
	}

	// 3. 计算并应用差异
	diff := model.Compute(target, actual)
	if diff.Empty() {
		run.runner.completeAfterVerify(run.ctx, run, targetVersion)
		return
	}
	if err := run.step("push", func() error { return run.pushDiff(diff) }); err != nil {
		// 后端必须把整批差异放进**一次原子提交**：失败则设备保持原样，无需控制器回滚。
		run.finish(store.StatusFailed, "push_failed", err.Error(), true)
		return
	}

	// 4. 再拉一次验证收敛（verified 语义依赖这一步）
	if err := run.verifyConverged(target); err != nil {
		run.finish(store.StatusFailed, "verify_failed", err.Error(), true)
		return
	}

	// 5. 收尾：持久化并标记 verified。
	run.runner.completeAfterVerify(run.ctx, run, targetVersion)
}

// applyTarget 解析本操作应达到的目标模型与版本号。
func (run *opRun) applyTarget() (*model.DeviceConfig, int64, error) {
	// service 在提交期望态时记录了"本操作基于的版本"，故目标版本 = 它 + 1。
	if run.op.BaseVersion != nil {
		version := *run.op.BaseVersion + 1
		raw, err := run.runner.deps.Store.ConfigVersion(run.ctx, run.device.ID, version)
		if err == nil {
			var cfg model.DeviceConfig
			if uerr := json.Unmarshal([]byte(raw), &cfg); uerr != nil {
				return nil, 0, fmt.Errorf("目标版本 %d 的模型损坏: %w", version, uerr)
			}
			return &cfg, version, nil
		}
		// 历史版本缺失 → 退回当前期望态
		run.addFinding(fmt.Sprintf("目标版本 v%d 不可读，退回当前期望态", version))
	}
	cfg, version, err := run.runner.deps.State.Snapshot(run.device.ID)
	if err != nil {
		return nil, 0, fmt.Errorf("读取期望态失败: %w", err)
	}
	return cfg, version, nil
}
