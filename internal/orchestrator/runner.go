// Package orchestrator 是执行引擎：每交换机一条 FIFO 队列，串行执行两类操作——
//
//   - apply（快路径）：把业务变更提交时渲染好的命令直接推送到设备
//     （不拉全量配置、无快照、失败不回退；失败标记 drifted，交给对账）
//   - reconcile（对账）：拉配置 → 解析 → diff(权威模型, 实际)
//     → 推送差异 → 再拉验证 → 更新同步状态
//
// 清理依据是设备侧标记：对账只删除"带标记且不在权威模型"的对象。
package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"switchctl/internal/config"
	"switchctl/internal/device/driver"
	"switchctl/internal/state"
	"switchctl/internal/store"
)

// DeviceFactory 由上层注入：解析凭据并构造厂商驱动。
type DeviceFactory func(ctx context.Context, deviceName string) (driver.Device, *config.Device, error)

// Deps 是编排器依赖。
type Deps struct {
	Config *config.Config
	Store  *store.Store
	State  *state.Manager
	Logger *slog.Logger
	Open   DeviceFactory
	// Probe 完整执行一次设备初始化（拨号/认证/enable/关分页）后关闭会话；
	// 为 nil 时直接触发首轮对账。
	Probe func(ctx context.Context, deviceName string) error
}

// Runner 执行操作。
type Runner struct {
	deps Deps
	log  *slog.Logger

	mu sync.Mutex
	// queues 是每台设备的待执行队列；reservations 是已预留但尚未入队的槽位数。
	// 两者相加不能超过队列容量，这样“占位”与“入队”之间不会超卖。
	queues       map[int64]chan string
	reservations map[int64]int
	stop         chan struct{}
	// stopCancel 取消启动阶段的设备初始化等待；Stop 时调用。
	stopCancel context.CancelFunc
	wg         sync.WaitGroup
}

// New 构造 Runner（不启动）。
func New(deps Deps) *Runner {
	if deps.Logger == nil {
		deps.Logger = slog.Default()
	}
	return &Runner{
		deps:         deps,
		log:          deps.Logger,
		queues:       map[int64]chan string{},
		reservations: map[int64]int{},
		stop:         make(chan struct{}),
	}
}

// Start 启动执行引擎：回收中断操作，并完成所有设备的初始化与首轮对账投递。
// 任一设备初始化失败即返回错误（由调用方决定退出），不会跳过对账继续运行。
//
// 这里没有周期性对账：漂移发现依赖人工/CI 触发的对账。
func (r *Runner) Start(ctx context.Context) error {
	ctx, r.stopCancel = context.WithCancel(ctx)
	r.recoverInterrupted(ctx)
	return r.initialReconcile(ctx)
}

// initialReconcile 并行完成所有设备的初始化；全部就绪后才为每台设备投递首轮对账。
// 任一设备初始化失败会取消其余等待并返回错误；初始化期间不产生操作记录。
func (r *Runner) initialReconcile(ctx context.Context) error {
	initCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	var (
		wg       sync.WaitGroup
		mu       sync.Mutex
		firstErr error
	)
	for i := range r.deps.Config.Devices {
		name := r.deps.Config.Devices[i].Name
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := r.waitDeviceReady(initCtx, name); err != nil {
				mu.Lock()
				if firstErr == nil {
					firstErr = fmt.Errorf("设备 %s 初始化失败: %w", name, err)
				}
				mu.Unlock()
				cancel() // 一个设备失败就不再等其余设备
			}
		}()
	}
	wg.Wait()
	if firstErr != nil {
		return firstErr
	}

	// 全部设备就绪，触发首轮对账（建立 verified 基线）。
	for i := range r.deps.Config.Devices {
		name := r.deps.Config.Devices[i].Name
		row, err := r.deps.Store.GetDeviceByName(ctx, name)
		if err != nil {
			return fmt.Errorf("启动对账：设备 %s 记录不存在: %w", name, err)
		}
		if _, err := r.EnqueueReconcile(ctx, row.ID); err != nil {
			return fmt.Errorf("启动对账：设备 %s 投递失败: %w", name, err)
		}
		r.log.Info("设备初始化完成，已触发首轮对账", "device", name)
	}
	return nil
}

// waitDeviceReady 执行真实的设备初始化过程，可重试错误按 1s→2s→…→30s 退避。
// 设备初始化窗口为 5 分钟；确定性错误（认证失败、主机密钥不匹配等）立即返回。
func (r *Runner) waitDeviceReady(ctx context.Context, name string) error {
	const (
		window   = 5 * time.Minute
		maxDelay = 30 * time.Second
	)
	deadline := time.Now().Add(window)
	delay := time.Second

	for {
		var err error
		if r.deps.Probe != nil {
			err = r.deps.Probe(ctx, name)
		}
		if err == nil {
			return nil
		}
		if !driver.IsTransient(err) {
			return fmt.Errorf("设备初始化失败（不重试）: %w", err)
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("等待设备就绪超时（%s）: %w", window, err)
		}
		r.log.Debug("设备尚未就绪，退避后重试初始化",
			"device", name, "error", err, "delay", delay)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(delay):
		}
		if delay *= 2; delay > maxDelay {
			delay = maxDelay
		}
	}
}

// Stop 停止接收新操作并等待在途操作（由调用方控制超时）。
func (r *Runner) Stop() {
	close(r.stop)
	if r.stopCancel != nil {
		r.stopCancel()
	}
	r.wg.Wait()
}

// ErrQueueFull 表示该设备的待执行队列已满。
// 语义：请求被拒绝且未入队，调用方应退避重试；绝不阻塞 HTTP 请求。
var ErrQueueFull = errors.New("操作队列已满")

// errEngineStopped 表示执行引擎已停止，不再接收新操作。
var errEngineStopped = errors.New("执行引擎已停止")

// Reserve 为非阻塞入队预留一个槽位；队列满或引擎已停止时返回错误。
// 预留成功后必须调用 SubmitReserved 消费槽位；否则在放弃时调用 Release。
//
// 预留发生在提交期望态之前，从而避免“期望态已提交、但入队因队列满而失败”的裂缝。
func (r *Runner) Reserve(deviceID int64) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	select {
	case <-r.stop:
		return errEngineStopped
	default:
	}
	q := r.queueLocked(deviceID)
	if len(q)+r.reservations[deviceID] >= cap(q) {
		return fmt.Errorf("%w（该设备队列长度上限 %d）", ErrQueueFull, cap(q))
	}
	r.reservations[deviceID]++
	return nil
}

// Release 归还一个预留槽位（预留后未能入队时调用）。
func (r *Runner) Release(deviceID int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.reservations[deviceID] > 0 {
		r.reservations[deviceID]--
	}
}

// SubmitReserved 把已预留槽位的操作送入队列；槽位在本次调用中消费。
// 容量已在 Reserve 阶段锁定，因此正常情况下不会因队列满而失败。
func (r *Runner) SubmitReserved(ctx context.Context, deviceID int64, opID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	q := r.queueLocked(deviceID)
	if r.reservations[deviceID] > 0 {
		r.reservations[deviceID]--
	}
	select {
	case <-r.stop:
		return errEngineStopped
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	select {
	case q <- opID:
		return nil
	default:
		// 理论上不会发生（容量已在 Reserve 阶段预留）。
		return fmt.Errorf("%w（该设备队列长度上限 %d）", ErrQueueFull, cap(q))
	}
}

// QueueDepth 返回设备当前待执行的操作数（含已预留未入队的槽位，指标用）。
func (r *Runner) QueueDepth(deviceID int64) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	q := r.queues[deviceID]
	if q == nil {
		return 0
	}
	return len(q) + r.reservations[deviceID]
}

// EnqueueReconcile 创建并投递一次对账操作。
func (r *Runner) EnqueueReconcile(ctx context.Context, deviceID int64) (*store.Operation, error) {
	// 先预留槽位，避免操作插入后因入队失败而留下一条失败记录。
	if err := r.Reserve(deviceID); err != nil {
		return nil, err
	}
	op := &store.Operation{
		ID:       store.NewOperationID(),
		DeviceID: deviceID,
		Kind:     store.KindReconcile,
		Status:   store.StatusPending,
	}
	if err := r.deps.Store.InsertOperation(ctx, op); err != nil {
		r.Release(deviceID)
		return nil, err
	}
	if err := r.SubmitReserved(ctx, deviceID, op.ID); err != nil {
		op.Status = store.StatusFailed
		op.FailureCode = "queue_submit_failed"
		op.FailureMessage = err.Error()
		_ = r.deps.Store.UpdateOperation(ctx, op)
		return nil, err
	}
	return op, nil
}

// queueLocked 返回设备队列；不存在时创建并启动该设备的 worker（调用方必须持有 r.mu）。
func (r *Runner) queueLocked(deviceID int64) chan string {
	q, ok := r.queues[deviceID]
	if !ok {
		q = make(chan string, r.deps.Config.Execution.MaxQueueDepth)
		r.queues[deviceID] = q
		r.wg.Add(1)
		go r.deviceWorker(deviceID, q)
	}
	return q
}

// deviceWorker 是单设备的串行执行者：严格按提交顺序消费队列。
func (r *Runner) deviceWorker(deviceID int64, q chan string) {
	defer r.wg.Done()
	for {
		select {
		case <-r.stop:
			return
		case opID := <-q:
			r.runOperation(deviceID, opID)
		}
	}
}

func (r *Runner) runOperation(deviceID int64, opID string) {
	log := r.log.With("op", opID)
	defer func() {
		if rec := recover(); rec != nil {
			log.Error("操作 panic", "panic", rec)
			if op, err := r.deps.Store.GetOperation(context.Background(), opID); err == nil {
				op.Status = store.StatusFailed
				op.FailureCode = "internal_panic"
				op.FailureMessage = fmt.Sprint(rec)
				_ = r.deps.Store.UpdateOperation(context.Background(), op)
			}
		}
	}()

	op, err := r.deps.Store.GetOperation(context.Background(), opID)
	if err != nil {
		log.Error("操作不存在", "error", err)
		return
	}
	deviceRow, err := r.deps.Store.GetDeviceByID(context.Background(), deviceID)
	if err != nil {
		log.Error("操作对应设备不存在", "error", err)
		return
	}
	cfgDev, ok := r.deps.Config.DeviceByName(deviceRow.Name)
	if !ok {
		r.failOperation(op, "device_missing", "设备不在配置清单中")
		return
	}
	now := time.Now()
	op.Status = store.StatusRunning
	op.StartedAt = &now
	op.Phase = "connect"
	_ = r.deps.Store.UpdateOperation(context.Background(), op)

	run := &opRun{
		runner: r,
		log:    log.With("device", deviceRow.Name, "kind", op.Kind),
		op:     op,
		device: deviceRow,
		cfgDev: cfgDev,
	}
	timeout := r.deps.Config.Execution.OperationTimeout.Std()
	if timeout <= 0 {
		// 防御性默认：配置未走 Load（或显式写 0）时不能立刻超时。
		timeout = 10 * time.Minute
	}
	run.ctx, run.cancel = context.WithTimeout(context.Background(), timeout)
	defer run.cancel()

	switch op.Kind {
	case store.KindApply:
		run.apply()
	case store.KindReconcile:
		run.reconcile()
	default:
		run.finish(store.StatusFailed, "unknown_kind", "未知操作类型: "+op.Kind, false)
	}
}

// failOperation 把未进入执行阶段的操作标记为失败。
func (r *Runner) failOperation(op *store.Operation, code, msg string) {
	now := time.Now()
	op.Status = store.StatusFailed
	op.FailureCode = code
	op.FailureMessage = msg
	op.FinishedAt = &now
	_ = r.deps.Store.UpdateOperation(context.Background(), op)
}

// --- 启动恢复与首轮对账 ---

// recoverInterrupted 回收上次进程中断的操作：标记 interrupted 并把设备置为 drifted。
// 注意：这里**不盲目重跑**中断的操作——设备可能停在半途。恢复方式是随后为受影响
// 设备触发一次对账（先拉取比对再决定要不要补），这与"按阶段定向恢复"等价且安全。
func (r *Runner) recoverInterrupted(ctx context.Context) {
	ops, err := r.deps.Store.ListOperationsByStatus(ctx, store.StatusPending, store.StatusRunning)
	if err != nil {
		r.log.Error("启动恢复：查询未完成操作失败", "error", err)
		return
	}
	for _, op := range ops {
		now := time.Now()
		op.Status = store.StatusInterrupted
		op.FailureCode = "controller_restart"
		op.FailureMessage = "控制器重启，操作在执行中被中断（设备实际状态由对账收敛）"
		op.FinishedAt = &now
		if err := r.deps.Store.UpdateOperation(ctx, op); err != nil {
			r.log.Error("启动恢复：标记失败", "op", op.ID, "error", err)
			continue
		}
		if err := r.deps.State.SetSync(ctx, op.DeviceID, store.SyncDrifted, nil); err != nil &&
			!errors.Is(err, store.ErrNotFound) {
			r.log.Warn("启动恢复：标记 drifted 失败", "op", op.ID, "error", err)
		}
	}
	if len(ops) > 0 {
		r.log.Warn("启动恢复：已回收中断操作", "count", len(ops))
	}
}

// completeAfterVerify 在"下发 + 验证通过"之后收尾：持久化并标记 verified。
func (r *Runner) completeAfterVerify(ctx context.Context, run *opRun, version int64) {
	if err := r.persist(run.ctx, run.conn); err != nil {
		run.finish(store.StatusFailed, "commit_failed",
			"配置已生效但持久化失败："+err.Error(), true)
		return
	}
	if err := r.deps.State.MarkVerified(ctx, run.device.ID, version); err != nil {
		r.log.Warn("记录已验证版本失败", "device", run.device.Name, "error", err)
	}
	run.finish(store.StatusSucceeded, "", "", false)
}

// persist 把运行配置持久化到设备的启动配置。
func (r *Runner) persist(ctx context.Context, dev driver.Device) error {
	return dev.Commit(ctx)
}

// GetOperation 读取一条操作（供 service 层做前置判断）。
func (r *Runner) GetOperation(ctx context.Context, id string) (*store.Operation, error) {
	return r.deps.Store.GetOperation(ctx, id)
}
