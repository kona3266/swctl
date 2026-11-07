package httpapi

import (
	"net/http"
)

func (s *Server) handleListSwitches(w http.ResponseWriter, r *http.Request) {
	items, err := s.deps.Service.ListSwitches(r.Context())
	if err != nil {
		writeError(w, s.deps.Logger, err)
		return
	}
	writeJSON(w, s.deps.Logger, http.StatusOK, map[string]any{"switches": items})
}
