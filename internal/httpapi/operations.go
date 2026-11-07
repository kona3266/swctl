package httpapi

import (
	"encoding/json"
	"net/http"
	"time"

	"switchctl/internal/service"
	"switchctl/internal/store"
)

// operationDTO 是异步操作的对外表示。
type operationDTO struct {
	ID             string          `json:"id"`
	Switch         string          `json:"switch"`
	Kind           string          `json:"kind"` // apply | reconcile
	Resource       string          `json:"resource,omitempty"`
	Name           string          `json:"name,omitempty"`
	Action         string          `json:"action,omitempty"`
	Status         string          `json:"status"`
	Phase          string          `json:"phase,omitempty"`
	Changes        json.RawMessage `json:"changes,omitempty"`
	Delta          json.RawMessage `json:"delta,omitempty"`
	FailureCode    string          `json:"failure_code,omitempty"`
	FailureMessage string          `json:"failure_message,omitempty"`
	CreatedAt      time.Time       `json:"created_at"`
	StartedAt      *time.Time      `json:"started_at,omitempty"`
	FinishedAt     *time.Time      `json:"finished_at,omitempty"`
	DurationMS     *int64          `json:"duration_ms,omitempty"`
}

func (s *Server) operationDTO(op *store.Operation, withDelta bool) *operationDTO {
	dto := &operationDTO{
		ID: op.ID, Switch: op.DeviceName, Kind: op.Kind, Status: op.Status, Phase: op.Phase,
		FailureCode: op.FailureCode, FailureMessage: op.FailureMessage,
		CreatedAt: op.CreatedAt, StartedAt: op.StartedAt, FinishedAt: op.FinishedAt,
		DurationMS: op.DurationMs,
	}
	if op.BizMeta != "" {
		var meta service.OperationMeta
		if err := json.Unmarshal([]byte(op.BizMeta), &meta); err == nil {
			dto.Resource, dto.Name, dto.Action = meta.Resource, meta.Name, meta.Action
		}
	}
	if op.Changes != "" && json.Valid([]byte(op.Changes)) {
		dto.Changes = json.RawMessage(op.Changes)
	}
	if withDelta && op.DeltaJSON != "" && json.Valid([]byte(op.DeltaJSON)) {
		dto.Delta = json.RawMessage(op.DeltaJSON)
	}
	return dto
}

// handleGetOperation 查询单个操作（含抽象差异）。
func (s *Server) handleGetOperation(w http.ResponseWriter, r *http.Request) {
	op, err := s.deps.Service.GetOperation(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, s.deps.Logger, err)
		return
	}
	writeJSON(w, s.deps.Logger, http.StatusOK, s.operationDTO(op, true))
}

// handleListOperations 查询操作列表：?switch=&status=&kind=&limit=
func (s *Server) handleListOperations(w http.ResponseWriter, r *http.Request) {
	ops, err := s.deps.Service.ListOperations(r.Context(), store.OperationFilter{
		DeviceName: r.URL.Query().Get("switch"),
		Status:     r.URL.Query().Get("status"),
		Kind:       r.URL.Query().Get("kind"),
		Limit:      intParam(r, "limit", 50),
	})
	if err != nil {
		writeError(w, s.deps.Logger, err)
		return
	}
	items := make([]*operationDTO, 0, len(ops))
	for _, op := range ops {
		items = append(items, s.operationDTO(op, false))
	}
	writeJSON(w, s.deps.Logger, http.StatusOK, map[string]any{"operations": items})
}
