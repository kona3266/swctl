package httpapi

import (
	"net/http"

	"switchctl/internal/model"
)

// bgpNeighborRequest 是创建/更新 BGP 邻居的业务参数。
//
// 字段为指针：缺省表示“本次不修改”，显式给出则覆盖（含显式清空，
// 如 "password": "" 清除密钥、"max_prefix": 0 取消限制）。
type bgpNeighborRequest struct {
	RemoteAS             *string `json:"remote_as"`
	SourceIP             *string `json:"source_ip"`
	Password             *string `json:"password"`
	BFD                  *bool   `json:"bfd"`
	BFDMinRxMS           *int    `json:"bfd_min_rx_ms"`
	BFDMinTxMS           *int    `json:"bfd_min_tx_ms"`
	BFDMultiplier        *int    `json:"bfd_multiplier"`
	MaxPrefix            *int    `json:"max_prefix"`
	MaxPrefixWarningOnly *bool   `json:"max_prefix_warning_only"`
	RouteReflectorClient *bool   `json:"route_reflector_client"`
}

func (q bgpNeighborRequest) params() model.BGPPeerParams {
	return model.BGPPeerParams{
		RemoteAS:             q.RemoteAS,
		SourceIP:             q.SourceIP,
		Password:             q.Password,
		BFD:                  q.BFD,
		BFDMinRxMS:           q.BFDMinRxMS,
		BFDMinTxMS:           q.BFDMinTxMS,
		BFDMultiplier:        q.BFDMultiplier,
		MaxPrefix:            q.MaxPrefix,
		MaxPrefixWarningOnly: q.MaxPrefixWarningOnly,
		RouteReflectorClient: q.RouteReflectorClient,
	}
}

// bgpNeighborView 是邻居的对外表示：密码只回显是否已设置，不回显明文。
type bgpNeighborView struct {
	VRF                  string `json:"vrf"`
	Neighbor             string `json:"neighbor"`
	RemoteAS             string `json:"remote_as"`
	SourceIP             string `json:"source_ip,omitempty"`
	PasswordSet          bool   `json:"password_set"`
	BFD                  bool   `json:"bfd"`
	BFDMinRxMS           int    `json:"bfd_min_rx_ms,omitempty"`
	BFDMinTxMS           int    `json:"bfd_min_tx_ms,omitempty"`
	BFDMultiplier        int    `json:"bfd_multiplier,omitempty"`
	MaxPrefix            int    `json:"max_prefix,omitempty"`
	MaxPrefixWarningOnly bool   `json:"max_prefix_warning_only,omitempty"`
	RouteReflectorClient bool   `json:"route_reflector_client,omitempty"`
	Activate             bool   `json:"activate"`
	Managed              bool   `json:"managed"`
}

func newBGPPeerView(p model.BGPPeer) bgpNeighborView {
	return bgpNeighborView{
		VRF:                  p.VRF,
		Neighbor:             p.Neighbor,
		RemoteAS:             p.RemoteAS,
		SourceIP:             p.SourceIP,
		PasswordSet:          p.Password != "",
		BFD:                  p.BFD,
		BFDMinRxMS:           p.BFDMinRxMS,
		BFDMinTxMS:           p.BFDMinTxMS,
		BFDMultiplier:        p.BFDMultiplier,
		MaxPrefix:            p.MaxPrefix,
		MaxPrefixWarningOnly: p.MaxPrefixWarningOnly,
		RouteReflectorClient: p.RouteReflectorClient,
		Activate:             p.Activate,
		Managed:              p.Managed,
	}
}

func (s *Server) handleListBGPNeighbors(w http.ResponseWriter, r *http.Request) {
	peers, err := s.deps.Service.ListBGPNeighbors(r.PathValue("name"), r.PathValue("vrf"))
	if err != nil {
		writeError(w, s.deps.Logger, err)
		return
	}
	out := make([]bgpNeighborView, 0, len(peers))
	for _, p := range peers {
		out = append(out, newBGPPeerView(p))
	}
	writeJSON(w, s.deps.Logger, http.StatusOK, map[string]any{"bgp_neighbors": out})
}

func (s *Server) handlePutBGPNeighbor(w http.ResponseWriter, r *http.Request) {
	var req bgpNeighborRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, s.deps.Logger, err)
		return
	}
	op, err := s.deps.Service.PutBGPNeighbor(r.Context(), r.PathValue("name"),
		r.PathValue("vrf"), r.PathValue("neighbor"), req.params(), remoteIP(r), expectedVersion(r))
	if err != nil {
		writeError(w, s.deps.Logger, err)
		return
	}
	s.respondOperation(w, r, op)
}

func (s *Server) handleDeleteBGPNeighbor(w http.ResponseWriter, r *http.Request) {
	op, err := s.deps.Service.DeleteBGPNeighbor(r.Context(), r.PathValue("name"),
		r.PathValue("vrf"), r.PathValue("neighbor"), remoteIP(r), expectedVersion(r))
	if err != nil {
		writeError(w, s.deps.Logger, err)
		return
	}
	s.respondOperation(w, r, op)
}
