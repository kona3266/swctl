package orchestrator

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"switchctl/internal/config"
	"switchctl/internal/device/dialect"
	"switchctl/internal/device/driver"
	"switchctl/internal/model"
	"switchctl/internal/store"
)

// opRun 承载"一次操作执行"会用到的全部状态：操作记录、目标设备记录、
// 设备配置（YAML）、方言实现、已建立的会话连接、步骤计数与执行期发现。
type opRun struct {
	runner *Runner
	log    *slog.Logger
	ctx    context.Context
	cancel context.CancelFunc

	op     *store.Operation
	device *store.Device  // devices 表记录
	cfgDev *config.Device // YAML 清单中的设备配置
	conn   driver.Device  // 已打开的后端（connect 步骤后可用）

	stepSeq  int
	findings []string
}

func (run *opRun) step(name string, fn func() error) error {
	run.stepSeq++
	stepID, err := run.runner.deps.Store.InsertStep(run.ctx, run.op.ID, run.stepSeq, name)
	if err != nil {
		run.log.Error("记录步骤失败", "error", err)
	}
	run.op.Phase = name
	_ = run.runner.deps.Store.UpdateOperation(run.ctx, run.op)

	start := time.Now()
	runErr := fn()
	status, errMsg := store.StepOK, ""
	if runErr != nil {
		status = store.StepFailed
		errMsg = runErr.Error()
	}
	if stepID > 0 {
		if err := run.runner.deps.Store.FinishStep(run.ctx, stepID, status, "", errMsg); err != nil {
			run.log.Error("完成步骤记录失败", "error", err)
		}
	}
	run.log.Info("步骤完成", "step", name, "ok", runErr == nil,
		"duration", time.Since(start), "detail", errMsg)
	return runErr
}

func (run *opRun) addFinding(s string) { run.findings = append(run.findings, s) }

// finish 落盘终态；drifted 为 true 时把设备标记为"期望未落地"。
func (run *opRun) finish(status, failureCode, failureMsg string, drifted bool) {
	// apply 失败时直接回退权威态到本操作之前，避免 desired 停在未落地的版本。
	if status == store.StatusFailed && run.op.Kind == store.KindApply {
		run.runner.rollbackDesired(context.Background(), run.op, run.device.ID)
	}
	now := time.Now()
	run.op.Status = status
	run.op.FailureCode = failureCode
	run.op.FailureMessage = failureMsg
	run.op.FinishedAt = &now
	if run.op.StartedAt != nil {
		ms := now.Sub(*run.op.StartedAt).Milliseconds()
		run.op.DurationMs = &ms
	}
	if err := run.runner.deps.Store.UpdateOperation(run.ctx, run.op); err != nil {
		run.log.Error("落盘操作终态失败", "error", err)
	}

	// 成功（含快路径乐观成功）记为 synced；未落地则 drifted。
	syncState := store.SyncSynced
	var reconciledAt *time.Time = &now
	if drifted {
		syncState = store.SyncDrifted
		reconciledAt = nil
	}
	if err := run.runner.deps.State.SetSync(run.ctx, run.device.ID, syncState, reconciledAt); err != nil {
		run.log.Error("更新同步状态失败", "error", err)
	}

	run.log.Info("操作结束", "status", status, "failure_code", failureCode,
		"drifted", drifted, "message", failureMsg)
}

// connect 建立会话并完成初始化。
func (run *opRun) connect() error {
	return run.step("connect", func() error {
		if run.runner.deps.Open == nil {
			return fmt.Errorf("该平台的后端未实现，无法连接设备")
		}
		conn, _, err := run.runner.deps.Open(run.ctx, run.cfgDev.Name)
		if err != nil {
			_ = run.runner.deps.Store.MarkDeviceError(run.ctx, run.device.ID, err.Error())
			return fmt.Errorf("连接设备失败: %w", err)
		}
		run.conn = conn
		_ = run.runner.deps.Store.MarkDeviceSeen(run.ctx, run.device.ID)
		if err := conn.Open(run.ctx); err != nil {
			return fmt.Errorf("会话初始化失败: %w", err)
		}
		return nil
	})
}

// recordApplyResult 把变更单元与回显写入 operation_commands。
func (run *opRun) recordApplyResult(res *driver.ApplyResult) {
	if res == nil {
		return
	}
	cmds := make([]*store.OperationCommand, 0, len(res.Changes)*2)
	for i, cr := range res.Changes {
		cmds = append(cmds, &store.OperationCommand{
			OperationID: run.op.ID, Seq: i * 2, Direction: "send", Command: cr.Item,
		})
		if cr.Output != "" || cr.IsError {
			cmds = append(cmds, &store.OperationCommand{
				OperationID: run.op.ID, Seq: i*2 + 1, Direction: "recv",
				Output: cr.Output, IsError: cr.IsError, ErrorFlag: cr.ErrorFlag,
			})
		}
	}
	if err := run.runner.deps.Store.AppendCommands(run.ctx, cmds); err != nil {
		run.log.Warn("记录变更明细失败", "error", err)
	}
}

// pushDiff 把模型差异应用到设备；返回错误（含被设备拒绝的变更）或 nil。
func (run *opRun) pushDiff(d *model.Diff) error {
	res, err := run.conn.Apply(run.ctx, d)
	run.recordApplyResult(res)
	if err != nil {
		return fmt.Errorf("下发失败: %w", err)
	}
	if res != nil && res.Failed {
		return fmt.Errorf("变更被设备拒绝（%s）：%s", res.FailedItem, trimOneLine(res.ErrorText))
	}
	return nil
}

func trimOneLine(s string) string {
	if len(s) > 200 {
		return s[:200] + "…"
	}
	return s
}

// pullConfig 拉取设备当前配置（解析后的模型）。
func (run *opRun) pullConfig() (*model.DeviceConfig, error) {
	var actual *model.DeviceConfig
	err := run.step("pull", func() error {
		snap, err := run.conn.Pull(run.ctx)
		if err != nil {
			return fmt.Errorf("拉取配置失败: %w", err)
		}
		if snap.Config == nil {
			return fmt.Errorf("后端未返回解析后的配置模型")
		}
		actual = snap.Config
		if impl, ok := dialect.Get(run.cfgDev.Dialect); ok && !impl.AtomicApply {
			run.addFinding("平台后端未声明原子提交能力：下发失败可能留下半套配置")
		}
		return nil
	})
	return actual, err
}

// verifyConverged 再拉一次设备配置，确认已收敛到 target。
func (run *opRun) verifyConverged(target *model.DeviceConfig) error {
	return run.step("verify", func() error {
		snap, err := run.conn.Pull(run.ctx)
		if err != nil {
			return fmt.Errorf("验证时拉取配置失败: %w", err)
		}
		if snap.Config == nil {
			return fmt.Errorf("验证时后端未返回配置模型")
		}
		// 验证用 ComputeVerifiable：BGP 密码是只写字段，设备不回显，不能参与比对。
		if remaining := model.ComputeVerifiable(target, snap.Config); !remaining.Empty() {
			s := summarize(remaining)
			s.Remaining = remaining.Count()
			run.writeChanges(s)
			return fmt.Errorf("收敛未完成：仍差 %d 项（详见操作 changes）", remaining.Count())
		}
		return nil
	})
}
