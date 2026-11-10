package httpapi

import (
	"net/http"
	"strconv"
)

// portRequest 是纳管物理口/设置模式的业务参数。
// mode 缺省：新建默认 l3；已存在表示不修改。
type portRequest struct {
	Mode string `json:"mode,omitempty"`
}

// subInterfaceRequest 是创建/更新子接口的业务参数。
// vrf 缺省表示不修改绑定，"" 表示移回全局路由表。
type subInterfaceRequest struct {
	IP  string  `json:"ip"`
	VRF *string `json:"vrf,omitempty"`
}

func (s *Server) handleListPorts(w http.ResponseWriter, r *http.Request) {
	ports, err := s.deps.Service.ListPorts(r.PathValue("name"))
	if err != nil {
		writeError(w, s.deps.Logger, err)
		return
	}
	writeJSON(w, s.deps.Logger, http.StatusOK, map[string]any{"ports": ports})
}

func (s *Server) handlePutPort(w http.ResponseWriter, r *http.Request) {
	var req portRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, s.deps.Logger, err)
		return
	}
	op, err := s.deps.Service.PutPort(r.Context(), r.PathValue("name"),
		r.PathValue("port"), req.Mode, remoteIP(r), expectedVersion(r))
	if err != nil {
		writeError(w, s.deps.Logger, err)
		return
	}
	s.respondOperation(w, r, op)
}

func (s *Server) handleListSubInterfaces(w http.ResponseWriter, r *http.Request) {
	subs, err := s.deps.Service.ListSubInterfaces(r.PathValue("name"), r.PathValue("port"))
	if err != nil {
		writeError(w, s.deps.Logger, err)
		return
	}
	writeJSON(w, s.deps.Logger, http.StatusOK, map[string]any{"sub_interfaces": subs})
}

func (s *Server) handlePutSubInterface(w http.ResponseWriter, r *http.Request) {
	vlan, err := strconv.Atoi(r.PathValue("vlan"))
	if err != nil {
		writeError(w, s.deps.Logger, errBadRequest("invalid_request", "VLAN ID 必须是数字"))
		return
	}
	var req subInterfaceRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, s.deps.Logger, err)
		return
	}
	op, err := s.deps.Service.PutSubInterface(r.Context(), r.PathValue("name"),
		r.PathValue("port"), vlan, req.IP, req.VRF, remoteIP(r), expectedVersion(r))
	if err != nil {
		writeError(w, s.deps.Logger, err)
		return
	}
	s.respondOperation(w, r, op)
}

// subInterfaceVRFRequest 是子接口加入 VRF 的业务参数。
type subInterfaceVRFRequest struct {
	VRF string `json:"vrf"`
}

func (s *Server) handleJoinSubInterfaceVRF(w http.ResponseWriter, r *http.Request) {
	vlan, err := strconv.Atoi(r.PathValue("vlan"))
	if err != nil {
		writeError(w, s.deps.Logger, errBadRequest("invalid_request", "VLAN ID 必须是数字"))
		return
	}
	var req subInterfaceVRFRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, s.deps.Logger, err)
		return
	}
	op, err := s.deps.Service.JoinSubInterfaceVRF(r.Context(), r.PathValue("name"),
		r.PathValue("port"), vlan, req.VRF, remoteIP(r), expectedVersion(r))
	if err != nil {
		writeError(w, s.deps.Logger, err)
		return
	}
	s.respondOperation(w, r, op)
}

func (s *Server) handleLeaveSubInterfaceVRF(w http.ResponseWriter, r *http.Request) {
	vlan, err := strconv.Atoi(r.PathValue("vlan"))
	if err != nil {
		writeError(w, s.deps.Logger, errBadRequest("invalid_request", "VLAN ID 必须是数字"))
		return
	}
	op, err := s.deps.Service.LeaveSubInterfaceVRF(r.Context(), r.PathValue("name"),
		r.PathValue("port"), vlan, remoteIP(r), expectedVersion(r))
	if err != nil {
		writeError(w, s.deps.Logger, err)
		return
	}
	s.respondOperation(w, r, op)
}

func (s *Server) handleDeleteSubInterface(w http.ResponseWriter, r *http.Request) {
	vlan, err := strconv.Atoi(r.PathValue("vlan"))
	if err != nil {
		writeError(w, s.deps.Logger, errBadRequest("invalid_request", "VLAN ID 必须是数字"))
		return
	}
	op, err := s.deps.Service.DeleteSubInterface(r.Context(), r.PathValue("name"),
		r.PathValue("port"), vlan, remoteIP(r), expectedVersion(r))
	if err != nil {
		writeError(w, s.deps.Logger, err)
		return
	}
	s.respondOperation(w, r, op)
}
