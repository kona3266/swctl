package httpapi

import (
	"net/http"
	"strconv"
)

// sviRequest 是创建/更新 SVI 的业务参数。
type sviRequest struct {
	IP string `json:"ip"`
}

func (s *Server) handleListSVIs(w http.ResponseWriter, r *http.Request) {
	svis, err := s.deps.Service.ListSVIs(r.PathValue("name"))
	if err != nil {
		writeError(w, s.deps.Logger, err)
		return
	}
	writeJSON(w, s.deps.Logger, http.StatusOK, map[string]any{"vlan_interfaces": svis})
}

func (s *Server) handlePutSVI(w http.ResponseWriter, r *http.Request) {
	vlanID, err := strconv.Atoi(r.PathValue("vlan"))
	if err != nil {
		writeError(w, s.deps.Logger, errBadRequest("invalid_request", "VLAN ID 必须是数字"))
		return
	}
	var req sviRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, s.deps.Logger, err)
		return
	}
	op, err := s.deps.Service.PutSVI(r.Context(), r.PathValue("name"), vlanID, req.IP, remoteIP(r), expectedVersion(r))
	if err != nil {
		writeError(w, s.deps.Logger, err)
		return
	}
	s.respondOperation(w, r, op)
}

func (s *Server) handleDeleteSVI(w http.ResponseWriter, r *http.Request) {
	vlanID, err := strconv.Atoi(r.PathValue("vlan"))
	if err != nil {
		writeError(w, s.deps.Logger, errBadRequest("invalid_request", "VLAN ID 必须是数字"))
		return
	}
	op, err := s.deps.Service.DeleteSVI(r.Context(), r.PathValue("name"), vlanID, remoteIP(r), expectedVersion(r))
	if err != nil {
		writeError(w, s.deps.Logger, err)
		return
	}
	s.respondOperation(w, r, op)
}

// sviVRFRequest 是加入 VRF 的业务参数。
type sviVRFRequest struct {
	VRF string `json:"vrf"`
}

func (s *Server) handleJoinSVIVRF(w http.ResponseWriter, r *http.Request) {
	vlanID, err := strconv.Atoi(r.PathValue("vlan"))
	if err != nil {
		writeError(w, s.deps.Logger, errBadRequest("invalid_request", "VLAN ID 必须是数字"))
		return
	}
	var req sviVRFRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, s.deps.Logger, err)
		return
	}
	op, err := s.deps.Service.JoinSVIVRF(r.Context(), r.PathValue("name"), vlanID, req.VRF, remoteIP(r), expectedVersion(r))
	if err != nil {
		writeError(w, s.deps.Logger, err)
		return
	}
	s.respondOperation(w, r, op)
}

func (s *Server) handleLeaveSVIVRF(w http.ResponseWriter, r *http.Request) {
	vlanID, err := strconv.Atoi(r.PathValue("vlan"))
	if err != nil {
		writeError(w, s.deps.Logger, errBadRequest("invalid_request", "VLAN ID 必须是数字"))
		return
	}
	op, err := s.deps.Service.LeaveSVIVRF(r.Context(), r.PathValue("name"), vlanID, remoteIP(r), expectedVersion(r))
	if err != nil {
		writeError(w, s.deps.Logger, err)
		return
	}
	s.respondOperation(w, r, op)
}
