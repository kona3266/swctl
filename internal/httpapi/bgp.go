package httpapi

import (
	"net/http"
)

// bgpNeighborRequest 是创建/更新 BGP 邻居的业务参数。
type bgpNeighborRequest struct {
	RemoteAS string `json:"remote_as"`
}

func (s *Server) handleListBGPNeighbors(w http.ResponseWriter, r *http.Request) {
	peers, err := s.deps.Service.ListBGPNeighbors(r.PathValue("name"), r.PathValue("vrf"))
	if err != nil {
		writeError(w, s.deps.Logger, err)
		return
	}
	writeJSON(w, s.deps.Logger, http.StatusOK, map[string]any{"bgp_neighbors": peers})
}

func (s *Server) handlePutBGPNeighbor(w http.ResponseWriter, r *http.Request) {
	var req bgpNeighborRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, s.deps.Logger, err)
		return
	}
	op, err := s.deps.Service.PutBGPNeighbor(r.Context(), r.PathValue("name"),
		r.PathValue("vrf"), r.PathValue("neighbor"), req.RemoteAS, remoteIP(r), expectedVersion(r))
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
