package httpapi

import (
	"fmt"
	"net/http"
	"sort"
	"strings"
)

// handleMetrics 输出 Prometheus 文本格式的最小指标集。
//
// 稳定性靠可观测性兜底：这里只暴露"运维真正会看"的几个数——
// 各状态操作数、设备队列深度、desired/verified 版本差（未落地变更数）、
// 最近一次验证时间。
func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	var b strings.Builder

	// 操作计数（按 status）
	if ops, err := s.deps.Service.ListOperationsPage(r.Context(), 1000); err == nil {
		byStatus := map[string]int{}
		for _, op := range ops {
			byStatus[op.Status]++
		}
		keys := make([]string, 0, len(byStatus))
		for k := range byStatus {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		b.WriteString("# HELP switchctl_operations 最近操作按状态计数\n")
		b.WriteString("# TYPE switchctl_operations gauge\n")
		for _, k := range keys {
			fmt.Fprintf(&b, "switchctl_operations{status=%q} %d\n", k, byStatus[k])
		}
	}

	// 设备维度：desired/verified 差值 + 队列深度
	if infos, err := s.deps.Service.ListSwitches(r.Context()); err == nil {
		b.WriteString("# HELP switchctl_pending_changes 尚未验证落地的变更版本数（desired - verified）\n")
		b.WriteString("# TYPE switchctl_pending_changes gauge\n")
		for _, in := range infos {
			pending := in.Version - in.VerifiedVersion
			if pending < 0 {
				pending = 0
			}
			fmt.Fprintf(&b, "switchctl_pending_changes{switch=%q} %d\n", in.Name, pending)
			fmt.Fprintf(&b, "switchctl_sync_state{switch=%q,state=%q} 1\n", in.Name, in.SyncState)
			if in.VerifiedAt != nil {
				fmt.Fprintf(&b, "switchctl_last_verified_seconds{switch=%q} %d\n",
					in.Name, in.VerifiedAt.Unix())
			}
		}
		b.WriteString("# HELP switchctl_queue_depth 设备待执行操作队列长度\n")
		b.WriteString("# TYPE switchctl_queue_depth gauge\n")
		for _, q := range s.deps.Service.QueueDepths() {
			fmt.Fprintf(&b, "switchctl_queue_depth{switch=%q} %d\n", q.Name, q.Depth)
		}
	}

	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	_, _ = w.Write([]byte(b.String()))
}
