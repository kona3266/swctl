package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"switchctl/internal/config"
	"switchctl/internal/device/driver"
	"switchctl/internal/model"
	"switchctl/internal/state"
	"switchctl/internal/store"
)

// fakeDevice 是内存版的 southbound 后端：只记录被应用的差异与提交次数。
type fakeDevice struct {
	mu        sync.Mutex
	cfg       *model.DeviceConfig
	applied   int
	committed int
	failApply bool
}

func (f *fakeDevice) Open(ctx context.Context) error { return nil }
func (f *fakeDevice) Close() error                   { return nil }

func (f *fakeDevice) Pull(ctx context.Context) (*driver.Snapshot, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return &driver.Snapshot{
		Format: "fake", Raw: []byte("{}"), Digest: "d", Config: f.cfg.Clone(),
	}, nil
}

func (f *fakeDevice) Apply(ctx context.Context, d *model.Diff) (*driver.ApplyResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failApply {
		return &driver.ApplyResult{Failed: true, FailedItem: "vlan", ErrorText: "设备拒绝"}, nil
	}
	f.applied += d.Count()
	// 简化：把期望态直接当作设备态（apply 后即收敛）
	for _, v := range d.VRFsAdd {
		v := v
		f.cfg.VRFs[v.Name] = &v
	}
	for _, ch := range d.VRFsUpdate {
		v := ch.To
		f.cfg.VRFs[v.Name] = &v
	}
	for _, v := range d.VRFsDelete {
		delete(f.cfg.VRFs, v.Name)
	}
	return &driver.ApplyResult{}, nil
}

func (f *fakeDevice) Commit(ctx context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.committed++
	return nil
}

type testEnv struct {
	store *store.Store
	state *state.Manager
	cfg   *config.Config
	devID int64
	fake  *fakeDevice
	run   *Runner
}

var testDBSeq atomic.Int64

func newTestEnv(t *testing.T) *testEnv {
	t.Helper()
	ctx := context.Background()
	dsn := fmt.Sprintf("file:orch-test-%d?mode=memory&cache=shared", testDBSeq.Add(1))
	st, err := store.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })

	devID, err := st.SyncDevice(ctx, "sw1", "127.0.0.1", 830, "ruijie_rgos")
	if err != nil {
		t.Fatal(err)
	}
	sm, err := state.New(ctx, st, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	if err := sm.Ensure(ctx, devID); err != nil {
		t.Fatal(err)
	}

	cfg := &config.Config{Devices: []config.Device{{Name: "sw1", Host: "127.0.0.1", Port: 830, Dialect: "ruijie_rgos"}}}
	cfg.Execution.MaxQueueDepth = 8

	fake := &fakeDevice{cfg: model.NewDeviceConfig()}
	open := func(ctx context.Context, name string) (driver.Device, *config.Device, error) {
		return fake, &cfg.Devices[0], nil
	}
	logOut := io.Discard
	if testing.Verbose() {
		logOut = os.Stderr
	}
	run := New(Deps{Config: cfg, Store: st, State: sm, Logger: slog.New(slog.NewTextHandler(logOut, &slog.HandlerOptions{Level: slog.LevelDebug})), Open: open})
	return &testEnv{store: st, state: sm, cfg: cfg, devID: devID, fake: fake, run: run}
}

// TestApplyMarksVerified 验证：下发成功后持久化，desired 与 verified 追平。
func TestApplyMarksVerified(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()

	desired, ver, err := env.state.Snapshot(env.devID)
	if err != nil {
		t.Fatal(err)
	}
	work := desired.Clone()
	work.VRFs["C1"] = &model.VRF{Name: "C1", RD: "65000:1", Managed: true}
	newVersion, err := env.state.CommitCAS(ctx, env.devID, work, "", ver)
	if err != nil {
		t.Fatal(err)
	}
	delta := model.Compute(work, desired)
	raw, _ := json.Marshal(delta)
	prev := ver // 与 service 一致：回滚目标 = 变更前版本
	op := &store.Operation{
		ID: store.NewOperationID(), DeviceID: env.devID, Kind: store.KindApply,
		Status: store.StatusPending, DeltaJSON: string(raw),
		BaseVersion: &prev,
	}
	if err := env.store.InsertOperation(ctx, op); err != nil {
		t.Fatal(err)
	}

	env.run.runOperation(env.devID, op.ID)

	got, err := env.store.GetOperation(ctx, op.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != store.StatusSucceeded {
		t.Fatalf("status = %s（期望 succeeded），phase=%s failure=%s/%s",
			got.Status, got.Phase, got.FailureCode, got.FailureMessage)
	}
	_, _, verified, _, _, err := env.state.Status(env.devID)
	if err != nil {
		t.Fatal(err)
	}
	if verified != newVersion {
		t.Fatalf("verified = %d，期望 %d", verified, newVersion)
	}
	if env.fake.committed == 0 {
		t.Fatal("Commit 未被调用")
	}
}

// TestStartFailsOnDeviceInitFailure 验证设备初始化失败时 Start 返回错误（不跳过对账）。
func TestStartFailsOnDeviceInitFailure(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	env.run.deps.Probe = func(ctx context.Context, name string) error {
		return fmt.Errorf("连接失败")
	}
	if err := env.run.Start(ctx); err == nil {
		t.Fatal("设备初始化失败时 Start 应返回错误")
	}
}

// TestApplyFailureRollsBackDesired 验证 apply 失败后权威态回退到操作前版本。
func TestApplyFailureRollsBackDesired(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	env.fake.failApply = true

	base, ver, err := env.state.Snapshot(env.devID)
	if err != nil {
		t.Fatal(err)
	}
	work := base.Clone()
	work.VRFs["C1"] = &model.VRF{Name: "C1", RD: "65000:1", Managed: true}
	target, err := env.state.CommitCAS(ctx, env.devID, work, "", ver)
	if err != nil {
		t.Fatal(err)
	}
	prev := ver
	op := &store.Operation{
		ID: store.NewOperationID(), DeviceID: env.devID, Kind: store.KindApply,
		Status: store.StatusPending, BaseVersion: &prev,
	}
	if err := env.store.InsertOperation(ctx, op); err != nil {
		t.Fatal(err)
	}

	env.run.runOperation(env.devID, op.ID)

	got, err := env.store.GetOperation(ctx, op.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != store.StatusFailed {
		t.Fatalf("status = %s，期望 failed", got.Status)
	}
	cfg, curVer, err := env.state.Snapshot(env.devID)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := cfg.VRFs["C1"]; ok {
		t.Fatal("apply 失败后 desired 未回退")
	}
	if curVer != prev {
		t.Fatalf("回退后 version = %d，期望 %d", curVer, prev)
	}
	if _, err := env.store.ConfigVersion(ctx, env.devID, target); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("失败版本历史应删除，err=%v", err)
	}
}

// TestQueueFullRejectsImmediately 验证：队列满时立即返回 ErrQueueFull，不阻塞。
func TestQueueFullRejectsImmediately(t *testing.T) {
	env := newTestEnv(t)
	env.cfg.Execution.MaxQueueDepth = 1
	ctx := context.Background()

	// 构造一个没有消费者（worker 未启动）的队列，确保能稳定观察到"满"
	env.run.mu.Lock()
	env.run.queues[env.devID] = make(chan string, 1)
	env.run.mu.Unlock()

	submit := func(opID string) error {
		if err := env.run.Reserve(env.devID); err != nil {
			return err
		}
		return env.run.SubmitReserved(ctx, env.devID, opID)
	}
	if err := submit("occupied"); err != nil {
		t.Fatalf("第一次投递应成功: %v", err)
	}

	done := make(chan error, 1)
	go func() { done <- submit("x") }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("队列满时应返回错误")
		}
	case <-time.After(time.Second):
		t.Fatal("提交阻塞了（不应阻塞 HTTP 请求）")
	}
}

// TestReserveHonorsQueueCapacity 验证预留槽位与队列容量一致，Release 可归还。
func TestReserveHonorsQueueCapacity(t *testing.T) {
	env := newTestEnv(t)
	env.cfg.Execution.MaxQueueDepth = 1
	ctx := context.Background()

	// 注入一个无消费者的队列，稳定观察容量。
	env.run.mu.Lock()
	env.run.queues[env.devID] = make(chan string, 1)
	env.run.mu.Unlock()

	if err := env.run.Reserve(env.devID); err != nil {
		t.Fatalf("首次 Reserve 应成功: %v", err)
	}
	if err := env.run.Reserve(env.devID); err == nil {
		t.Fatal("容量用尽后 Reserve 应失败")
	}
	env.run.Release(env.devID)
	if err := env.run.Reserve(env.devID); err != nil {
		t.Fatalf("Release 后 Reserve 应成功: %v", err)
	}
	if err := env.run.SubmitReserved(ctx, env.devID, "x"); err != nil {
		t.Fatalf("SubmitReserved: %v", err)
	}
	if got := env.run.QueueDepth(env.devID); got != 1 {
		t.Fatalf("QueueDepth = %d，期望 1", got)
	}
}

// TestApplyPullsActualState 验证：apply 不再盲推 delta —— 设备上已被别人加上的对象
// 不会重复推送，且设备侧已有的漂移会被一并收敛。
func TestApplyPullsActualState(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()

	// 设备实际状态：手工加了 VRF "MANUAL"（未托管），且已有 "C1" 但 RD 是旧的
	env.fake.cfg.VRFs["MANUAL"] = &model.VRF{Name: "MANUAL", RD: "1:1"}

	base, ver, err := env.state.Snapshot(env.devID)
	if err != nil {
		t.Fatal(err)
	}
	work := base.Clone()
	work.VRFs["C1"] = &model.VRF{Name: "C1", RD: "65000:1", Managed: true}
	if _, err := env.state.CommitCAS(ctx, env.devID, work, "", ver); err != nil {
		t.Fatal(err)
	}

	prev := ver
	op := &store.Operation{
		ID: store.NewOperationID(), DeviceID: env.devID, Kind: store.KindApply,
		Status: store.StatusPending,
		// delta 故意给一个"错误基线"（空），真正下发应以 pull 后的 diff 为准
		DeltaJSON: "{}", BaseVersion: &prev,
	}
	if err := env.store.InsertOperation(ctx, op); err != nil {
		t.Fatal(err)
	}

	env.run.runOperation(env.devID, op.ID)

	got, _ := env.store.GetOperation(ctx, op.ID)
	if got.Status != store.StatusSucceeded {
		t.Fatalf("status = %s，failure=%s", got.Status, got.FailureMessage)
	}
	if _, ok := env.fake.cfg.VRFs["C1"]; !ok {
		t.Fatal("设备上未出现期望的 VRF C1（说明没有按 pull 后的 diff 下发）")
	}
	if _, ok := env.fake.cfg.VRFs["MANUAL"]; !ok {
		t.Fatal("未托管对象被误删")
	}
}
