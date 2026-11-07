package httpapi

import (
	"net/http"
)

// vrfRequest 是创建/更新 VRF 的业务参数。
type vrfRequest struct {
	RD string `json:"rd"`
}

func (s *Server) handleListVRFs(w http.ResponseWriter, r *http.Request) {
	vrfs, err := s.deps.Service.ListVRFs(r.PathValue("name"))
	if err != nil {
		writeError(w, s.deps.Logger, err)
		return
	}
	writeJSON(w, s.deps.Logger, http.StatusOK, map[string]any{"vrfs": vrfs})
}

func (s *Server) handlePutVRF(w http.ResponseWriter, r *http.Request) {
	var req vrfRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, s.deps.Logger, err)
		return
	}
	op, err := s.deps.Service.PutVRF(r.Context(), r.PathValue("name"), r.PathValue("vrf"), req.RD, remoteIP(r), expectedVersion(r))
	if err != nil {
		writeError(w, s.deps.Logger, err)
		return
	}
	s.respondOperation(w, r, op)
}

func (s *Server) handleDeleteVRF(w http.ResponseWriter, r *http.Request) {
	op, err := s.deps.Service.DeleteVRF(r.Context(), r.PathValue("name"), r.PathValue("vrf"), remoteIP(r), expectedVersion(r))
	if err != nil {
		writeError(w, s.deps.Logger, err)
		return
	}
	s.respondOperation(w, r, op)
}
