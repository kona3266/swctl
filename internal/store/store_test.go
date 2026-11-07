package store

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"
)

var testDBCounter atomic.Int64

func openTestStore(t *testing.T) *Store {
	t.Helper()
	dsn := fmt.Sprintf("file:switchctl-test-%d?mode=memory&cache=shared", testDBCounter.Add(1))
	st, err := Open(dsn)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func TestDevicesAndOperations(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)

	devID, err := st.SyncDevice(ctx, "sw-core-1", "10.0.0.11", 22, "cisco_ios")
	if err != nil {
		t.Fatalf("SyncDevice: %v", err)
	}
	dev, err := st.GetDeviceByName(ctx, "sw-core-1")
	if err != nil || dev.ID != devID || dev.Dialect != "cisco_ios" {
		t.Fatalf("GetDeviceByName = %+v, %v", dev, err)
	}

	op := &Operation{
		ID: NewOperationID(), DeviceID: devID, Kind: KindApply,
		Status: StatusPending, BizMeta: `{"resource":"vrf"}`,
	}
	if err := st.InsertOperation(ctx, op); err != nil {
		t.Fatalf("InsertOperation: %v", err)
	}

	got, err := st.GetOperation(ctx, op.ID)
	if err != nil {
		t.Fatalf("GetOperation: %v", err)
	}
	if got.DeviceName != "sw-core-1" || got.Status != StatusPending || got.BizMeta == "" {
		t.Fatalf("GetOperation = %+v", got)
	}

	list, err := st.ListOperations(ctx, OperationFilter{DeviceName: "sw-core-1", Status: StatusPending})
	if err != nil || len(list) != 1 || list[0].ID != op.ID {
		t.Fatalf("ListOperations = %+v, %v", list, err)
	}

	active, err := st.FindActiveOperation(ctx, devID, KindApply)
	if err != nil || active.ID != op.ID {
		t.Fatalf("FindActiveOperation = %+v, %v", active, err)
	}

	now := time.Now()
	op.Status = StatusSucceeded
	op.FinishedAt = &now
	if err := st.UpdateOperation(ctx, op); err != nil {
		t.Fatalf("UpdateOperation: %v", err)
	}
	if _, err := st.FindActiveOperation(ctx, devID, KindApply); !errors.Is(err, ErrNotFound) {
		t.Fatalf("已结束操作不应再算进行中: %v", err)
	}

	pending, err := st.ListOperationsByStatus(ctx, StatusPending, StatusRunning)
	if err != nil || len(pending) != 0 {
		t.Fatalf("ListOperationsByStatus = %+v, %v", pending, err)
	}
}

func TestConfigCommitAndVersions(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)
	devID, err := st.SyncDevice(ctx, "sw1", "127.0.0.1", 22, "cisco_ios")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.EnsureConfig(ctx, devID, `{"vrfs":{},"vlans":{},"svis":{},"bgp_peers":{}}`); err != nil {
		t.Fatal(err)
	}
	v1, err := st.CommitConfig(ctx, devID, `{"vrfs":{"A":{"name":"A","rd":"65000:1","managed":true}}}`, "op1")
	if err != nil || v1 != 1 {
		t.Fatalf("CommitConfig = %d, %v", v1, err)
	}
	v2, err := st.CommitConfig(ctx, devID, `{"vrfs":{}}`, "op2")
	if err != nil || v2 != 2 {
		t.Fatalf("CommitConfig = %d, %v", v2, err)
	}
	entries, err := st.ListConfigs(ctx)
	if err != nil || len(entries) != 1 || entries[0].Version != 2 {
		t.Fatalf("ListConfigs = %+v, %v", entries, err)
	}
	if err := st.SetSyncState(ctx, devID, SyncDrifted, nil); err != nil {
		t.Fatal(err)
	}
	entries, _ = st.ListConfigs(ctx)
	if entries[0].SyncState != SyncDrifted {
		t.Fatalf("SyncState = %q", entries[0].SyncState)
	}
}

func TestRollbackConfig(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)
	devID, err := st.SyncDevice(ctx, "sw1", "127.0.0.1", 22, "cisco_ios")
	if err != nil {
		t.Fatal(err)
	}
	const empty = `{"vrfs":{},"vlans":{},"svis":{},"bgp_peers":{}}`
	if err := st.EnsureConfig(ctx, devID, empty); err != nil {
		t.Fatal(err)
	}
	v1, err := st.CommitConfig(ctx, devID, `{"vrfs":{"A":{"name":"A","rd":"65000:1","managed":true}}}`, "op1")
	if err != nil || v1 != 1 {
		t.Fatalf("CommitConfig = %d, %v", v1, err)
	}
	if err := st.RollbackConfig(ctx, devID, v1, empty); err != nil {
		t.Fatalf("RollbackConfig: %v", err)
	}
	entries, err := st.ListConfigs(ctx)
	if err != nil || len(entries) != 1 || entries[0].Version != 0 || entries[0].ModelJSON != empty {
		t.Fatalf("回退后 = %+v, %v", entries, err)
	}
	// 版本 0 历史仍在，失败版本历史已删除。
	if _, err := st.ConfigVersion(ctx, devID, 0); err != nil {
		t.Fatalf("版本 0 历史应存在: %v", err)
	}
	if _, err := st.ConfigVersion(ctx, devID, v1); !errors.Is(err, ErrNotFound) {
		t.Fatalf("失败版本历史应删除，err=%v", err)
	}
}
