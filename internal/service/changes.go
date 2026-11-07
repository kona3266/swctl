package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"switchctl/internal/device/dialect"
	"switchctl/internal/model"
	"switchctl/internal/orchestrator"
	"switchctl/internal/store"
)

// OperationMeta 是操作的业务元数据（供操作查询接口展示）。
type OperationMeta struct {
	Resource string `json:"resource"` // vrf | vlan
	Name     string `json:"name"`     // VRF 名 / VLAN ID
	Action   string `json:"action"`   // apply | delete
}

func metaJSON(resource, name, action string) string {
	raw, _ := json.Marshal(OperationMeta{Resource: resource, Name: name, Action: action})
	return string(raw)
}

// applyChange 是业务变更的统一流程：
//
//	取权威态快照 → 在工作副本上应用变更（校验）→ 计算模型 delta →
//	创建操作 → 提交权威态（内存+DB）→ 入队异步推送。
//
// 操作的身份是 op.ID；对象身份是 resource+name（存于 biz_meta）。
func (s *Service) applyChange(
	ctx context.Context,
	switchName, sourceIP, resource, name, action string,
	expectedVersion int64,
	mutate func(cfg *model.DeviceConfig, c model.Constraints) error,
) (*store.Operation, error) {
	dev, err := s.deviceRow(switchName)
	if err != nil {
		return nil, err
	}
	unlock := s.lockDevice(dev.ID)
	defer unlock()

	if expectedVersion == -2 {
		return nil, errInvalid("If-Match / expected_version 必须是整数版本号")
	}
	impl, ok := dialect.Get(dev.Dialect)
	if !ok {
		return nil, errUnsupported("该交换机方言暂不支持此业务对象: " + dev.Dialect)
	}
	if err := s.state.Ensure(ctx, dev.ID); err != nil {
		return nil, err
	}

	// 权威态快照 → 工作副本上应用变更（校验在 mutate 内完成）
	oldModel, curVersion, err := s.state.Snapshot(dev.ID)
	if err != nil {
		return nil, err
	}
	working := oldModel.Clone()
	if err := mutate(working, impl.Constraints); err != nil {
		// 业务校验错误统一映射为 400/409；其他按内部错误上抛
		var se *Error
		if errors.As(err, &se) {
			return nil, se
		}
		return nil, errInvalid(err.Error())
	}

	changes := model.DescribeChanges(oldModel, working)
	if len(changes) == 0 {
		// 幂等：提交内容与权威态一致，无需触碰设备
		op := &store.Operation{
			ID: store.NewOperationID(), DeviceID: dev.ID, DeviceName: dev.Name,
			Kind: store.KindApply, Status: store.StatusNoChange, SourceIP: sourceIP,
			BizMeta: metaJSON(resource, name, action),
		}
		if err := s.store.InsertOperation(ctx, op); err != nil {
			return nil, err
		}
		return op, nil
	}

	// 同设备串行化：存在未完成的 apply 时拒绝新变更。
	// 这是“失败直接回退基版本”成立的前提：失败时 desired 一定恰好是该操作的目标版本。
	if _, err := s.store.FindActiveOperation(ctx, dev.ID, store.KindApply); err == nil {
		return nil, errConflict("该交换机有未完成的变更，请等待其完成后再提交")
	} else if !errors.Is(err, store.ErrNotFound) {
		return nil, err
	}

	// 留存"抽象变更"（模型 delta）：diff(新模型, 旧模型）。
	// 具体传输格式由 southbound 后端在执行时渲染（接口、命令、XML/JSON 均由后端决定）。
	delta := model.Compute(working, oldModel)
	changesJSON, _ := json.Marshal(changes)
	deltaJSON, _ := json.Marshal(delta)

	// 先预留队列槽位：队列满时在提交期望态之前就拒绝，
	// 避免出现“desired 已 +1、但操作因入队失败而永远不会执行”的裂缝。
	if err := s.runner.Reserve(dev.ID); err != nil {
		if errors.Is(err, orchestrator.ErrQueueFull) {
			return nil, &Error{Kind: "busy", Msg: "该交换机的操作队列已满，请稍后重试"}
		}
		return nil, &Error{Kind: "unavailable", Msg: "执行引擎不可用"}
	}
	reserved := true
	defer func() {
		if reserved {
			s.runner.Release(dev.ID)
		}
	}()

	op := &store.Operation{
		ID: store.NewOperationID(), DeviceID: dev.ID, DeviceName: dev.Name,
		Kind: store.KindApply, Status: store.StatusPending, SourceIP: sourceIP,
		BizMeta:   metaJSON(resource, name, action),
		Changes:   string(changesJSON),
		DeltaJSON: string(deltaJSON),
	}
	if err := s.store.InsertOperation(ctx, op); err != nil {
		return nil, err
	}

	// 提交期望态（先落库、再替换内存；失败则整体不生效）。
	// 带期望版本时做乐观并发控制：并发写入基于过期版本的请求会被拒绝。
	newVersion, err := s.state.CommitCAS(ctx, dev.ID, working, op.ID, expectedVersion)
	if err != nil {
		if errors.Is(err, store.ErrVersionConflict) {
			op.Status = store.StatusFailed
			op.FailureCode = "version_conflict"
			op.FailureMessage = err.Error()
			_ = s.store.UpdateOperation(ctx, op)
			return nil, errConflict(fmt.Sprintf(
				"配置已被其他请求修改（当前版本 %d），请重新读取后再提交", curVersion))
		}
		op.Status = store.StatusFailed
		op.FailureCode = "state_commit_failed"
		op.FailureMessage = err.Error()
		_ = s.store.UpdateOperation(ctx, op)
		return nil, fmt.Errorf("提交期望配置失败: %w", err)
	}
	// 记录本操作基于的版本：执行时目标版本 = base + 1（保证只提交本操作的参数）。
	if newVersion > 0 {
		base := newVersion - 1
		op.BaseVersion = &base
		if err := s.store.UpdateOperation(ctx, op); err != nil {
			s.log.Warn("记录操作基线版本失败", "op", op.ID, "error", err)
		}
	}
	// 入队异步执行。槽位已在提交前预留，SubmitReserved 会消费它；
	// 若此步失败（仅引擎停止/ctx 取消），期望态已提交，标记 drifted 等重启后对账收敛。
	reserved = false
	if err := s.runner.SubmitReserved(ctx, dev.ID, op.ID); err != nil {
		op.Status = store.StatusFailed
		op.FailureCode = "queue_submit_failed"
		op.FailureMessage = err.Error()
		_ = s.store.UpdateOperation(ctx, op)
		_ = s.state.SetSync(ctx, dev.ID, store.SyncDrifted, nil)
		return nil, &Error{Kind: "unavailable", Msg: "执行引擎不可用"}
	}
	s.log.Info("已接受业务变更（期望态已提交，等待异步推送）",
		"op", op.ID, "switch", dev.Name,
		"resource", resource, "name", name, "action", action)
	return op, nil
}
