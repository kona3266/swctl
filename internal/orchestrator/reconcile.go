package orchestrator

import (
	"encoding/json"
	"fmt"

	"switchctl/internal/model"
	"switchctl/internal/store"
)

// reconcileSummary 是写进 operations.changes 的对账摘要。
type reconcileSummary struct {
	VRFAdded   int `json:"vrf_added,omitempty"`
	VRFUpdated int `json:"vrf_updated,omitempty"`
	VRFDeleted int `json:"vrf_deleted,omitempty"`

	VLANAdded   int `json:"vlan_added,omitempty"`
	VLANUpdated int `json:"vlan_updated,omitempty"`
	VLANDeleted int `json:"vlan_deleted,omitempty"`

	SVIAdded   int `json:"svi_added,omitempty"`
	SVIUpdated int `json:"svi_updated,omitempty"`
	SVIDeleted int `json:"svi_deleted,omitempty"`

	BGPAdded   int `json:"bgp_peer_added,omitempty"`
	BGPUpdated int `json:"bgp_peer_updated,omitempty"`
	BGPDeleted int `json:"bgp_peer_deleted,omitempty"`

	UnmanagedVRFs     []string `json:"unmanaged_vrfs,omitempty"`
	UnmanagedVLANs    []int    `json:"unmanaged_vlans,omitempty"`
	UnmanagedSVIs     []int    `json:"unmanaged_svis,omitempty"`
	UnmanagedBGPPeers []string `json:"unmanaged_bgp_peers,omitempty"`
	Remaining         int      `json:"remaining,omitempty"`
	TotalChanges      int      `json:"total_changes,omitempty"`
}

func summarize(d *model.Diff) *reconcileSummary {
	return &reconcileSummary{
		VRFAdded:          len(d.VRFsAdd),
		VRFUpdated:        len(d.VRFsUpdate),
		VRFDeleted:        len(d.VRFsDelete),
		VLANAdded:         len(d.VLANsAdd),
		VLANUpdated:       len(d.VLANsUpdate),
		VLANDeleted:       len(d.VLANsDelete),
		SVIAdded:          len(d.SVIsAdd),
		SVIUpdated:        len(d.SVIsUpdate),
		SVIDeleted:        len(d.SVIsDelete),
		BGPAdded:          len(d.BGPPeersAdd),
		BGPUpdated:        len(d.BGPPeersUpdate),
		BGPDeleted:        len(d.BGPPeersDelete),
		UnmanagedVRFs:     d.UnmanagedVRFs,
		UnmanagedVLANs:    d.UnmanagedVLANs,
		UnmanagedSVIs:     d.UnmanagedSVIs,
		UnmanagedBGPPeers: d.UnmanagedBGPPeers,
	}
}

func (run *opRun) writeChanges(s *reconcileSummary) {
	raw, _ := json.Marshal(s)
	run.op.Changes = string(raw)
}

func (run *opRun) reconcile() {
	defer func() {
		if run.conn != nil {
			_ = run.conn.Close()
		}
	}()

	// 0. 先钉住权威态快照：本次收敛只把设备收敛到"启动时"的版本。
	//    收敛期间提交的新变更由排在队列后面的操作用自己的一次提交完成。
	desired, curVersion, err := run.runner.deps.State.Snapshot(run.device.ID)
	if err != nil {
		run.finish(store.StatusFailed, "state_missing", err.Error(), true)
		return
	}

	if err := run.connect(); err != nil {
		run.finish(store.StatusFailed, "device_unreachable", err.Error(), true)
		return
	}

	// 1. 拉配置
	actual, pullErr := run.pullConfig()
	if pullErr != nil {
		run.finish(store.StatusFailed, "reconcile_pull_failed", pullErr.Error(), true)
		return
	}

	// 2. 计算收敛差异（desired 是步骤 0 钉住的快照）
	diff := model.Compute(desired, actual)
	sum := summarize(diff)
	if diff.Empty() {
		run.writeChanges(sum)
		run.finish(store.StatusSucceeded, "", "", false)
		return
	}
	if len(sum.UnmanagedVRFs)+len(sum.UnmanagedVLANs)+len(sum.UnmanagedSVIs)+len(sum.UnmanagedBGPPeers) > 0 {
		run.addFinding(fmt.Sprintf("设备上存在未托管对象（仅报告不删除）: vrfs=%v vlans=%v svis=%v bgp_peers=%v",
			sum.UnmanagedVRFs, sum.UnmanagedVLANs, sum.UnmanagedSVIs, sum.UnmanagedBGPPeers))
	}

	// 3. 应用差异
	sum.TotalChanges = diff.Count()
	run.writeChanges(sum)
	if err := run.step("push", func() error { return run.pushDiff(diff) }); err != nil {
		// 整批差异在一次原子提交内执行；失败即设备未变，无需回滚。
		run.finish(store.StatusFailed, "reconcile_push_failed", err.Error(), true)
		return
	}

	// 5. 再拉一次验证收敛
	verifyErr := run.verifyConverged(desired)
	if verifyErr != nil {
		run.finish(store.StatusFailed, "verify_failed", verifyErr.Error(), true)
		return
	}

	run.runner.completeAfterVerify(run.ctx, run, curVersion)
}
