package orchestrator

import (
	"context"
	"encoding/json"

	"switchctl/internal/model"
	"switchctl/internal/store"
)

// rollbackDesired 把权威态回退到本操作之前的版本。
// 仅对 apply 生效；reconcile 不改变期望态。
func (r *Runner) rollbackDesired(ctx context.Context, op *store.Operation, deviceID int64) {
	if op.Kind != store.KindApply || op.BaseVersion == nil {
		return
	}
	base := *op.BaseVersion

	raw, err := r.deps.Store.ConfigVersion(ctx, deviceID, base)
	if err != nil {
		r.log.Warn("回退期望态：读取基线版本失败", "op", op.ID, "version", base, "error", err)
		return
	}
	var cfg model.DeviceConfig
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		r.log.Warn("回退期望态：基线版本模型损坏", "op", op.ID, "version", base, "error", err)
		return
	}
	if err := r.deps.State.Rollback(ctx, deviceID, base+1, &cfg); err != nil {
		r.log.Error("回退期望态失败", "op", op.ID, "failed_version", base+1, "error", err)
		return
	}
	r.log.Info("已回退期望态到操作前版本", "op", op.ID, "version", base)
}
