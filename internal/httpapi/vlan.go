package httpapi

import (
	"net/http"
	"strconv"
)

// vlanRequest 是创建/更新 VLAN 的业务参数。
type vlanRequest struct {
	Name string `json:"name,omitempty"`
}

func (s *Server) handleListVLANs(w http.ResponseWriter, r *http.Request) {
	vlans, err := s.deps.Service.ListVLANs(r.PathValue("name"))
	if err != nil {
		writeError(w, s.deps.Logger, err)
		return
	}
	writeJSON(w, s.deps.Logger, http.StatusOK, map[string]any{"vlans": vlans})
}

func (s *Server) handlePutVLAN(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(r.PathValue("id"))
	if err != nil {
		writeError(w, s.deps.Logger, errBadRequest("invalid_request", "VLAN ID 必须是数字"))
		return
	}
	var req vlanRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, s.deps.Logger, err)
		return
	}
	op, err := s.deps.Service.PutVLAN(r.Context(), r.PathValue("name"), id, req.Name, remoteIP(r), expectedVersion(r))
	if err != nil {
		writeError(w, s.deps.Logger, err)
		return
	}
	s.respondOperation(w, r, op)
}

func (s *Server) handleDeleteVLAN(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(r.PathValue("id"))
	if err != nil {
		writeError(w, s.deps.Logger, errBadRequest("invalid_request", "VLAN ID 必须是数字"))
		return
	}
	op, err := s.deps.Service.DeleteVLAN(r.Context(), r.PathValue("name"), id, remoteIP(r), expectedVersion(r))
	if err != nil {
		writeError(w, s.deps.Logger, err)
		return
	}
	s.respondOperation(w, r, op)
}
