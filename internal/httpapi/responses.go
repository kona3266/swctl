package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"switchctl/internal/service"
	"switchctl/internal/store"
)

// apiError 是带 HTTP 语义的错误。
type apiError struct {
	HTTP    int    `json:"-"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e *apiError) Error() string { return e.Code + ": " + e.Message }

func errBadRequest(code, msg string) *apiError {
	return &apiError{HTTP: http.StatusBadRequest, Code: code, Message: msg}
}

func writeJSON(w http.ResponseWriter, logger *slog.Logger, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if v == nil {
		return
	}
	if err := json.NewEncoder(w).Encode(v); err != nil {
		logger.Error("响应编码失败", "error", err)
	}
}

// writeError 把内部错误映射为 HTTP 响应。
func writeError(w http.ResponseWriter, logger *slog.Logger, err error) {
	if err == nil {
		return
	}
	var ae *apiError
	var se *service.Error
	switch {
	case errors.As(err, &ae):
		writeJSON(w, logger, ae.HTTP, ae)
	case errors.As(err, &se):
		writeJSON(w, logger, serviceErrorStatus(se.Kind), &apiError{Code: se.Kind, Message: se.Msg})
	default:
		logger.Error("内部错误", "error", err)
		writeJSON(w, logger, http.StatusInternalServerError,
			&apiError{Code: "internal", Message: "内部错误，详见服务端日志"})
	}
}

// serviceErrorStatus 把 service.Error.Kind 映射为 HTTP 状态码。
func serviceErrorStatus(kind string) int {
	switch kind {
	case "not_found":
		return http.StatusNotFound
	case "invalid":
		return http.StatusBadRequest
	case "not_managed", "conflict":
		return http.StatusConflict
	case "busy":
		return http.StatusTooManyRequests
	case "unsupported":
		return http.StatusUnprocessableEntity
	case "unavailable":
		return http.StatusServiceUnavailable
	default:
		return http.StatusInternalServerError
	}
}

// intParam 解析整数查询参数（非法或缺失时返回默认值）。
func intParam(r *http.Request, name string, def int) int {
	v := r.URL.Query().Get(name)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		return def
	}
	return n
}

// decodeJSON 解析请求体：拒绝未知字段与多余内容。
func decodeJSON(r *http.Request, dst any) error {
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return errBadRequest("invalid_json", fmt.Sprintf("请求体不是合法 JSON 或含未知字段: %v", err))
	}
	if dec.More() {
		return errBadRequest("invalid_json", "请求体含多余内容")
	}
	return nil
}

// respondOperation 统一返回操作句柄（202 + Location）；已完结的操作直接返回 200。
func (s *Server) respondOperation(w http.ResponseWriter, r *http.Request, op *store.Operation) {
	status := http.StatusAccepted
	if op.Status != store.StatusPending && op.Status != store.StatusRunning {
		status = http.StatusOK
	} else {
		w.Header().Set("Location", "/api/v1/operations/"+op.ID)
	}
	writeJSON(w, s.deps.Logger, status, s.operationDTO(op, false))
}

// expectedVersion 解析乐观并发控制的期望版本：
//   - 优先 If-Match（弱/强 ETag 均可，取其中的数字）；
//   - 其次查询参数 ?expected_version=N。
//
// 返回 -1 表示调用方未提供（不做冲突检查）。
func expectedVersion(r *http.Request) int64 {
	if v := strings.TrimSpace(r.Header.Get("If-Match")); v != "" {
		v = strings.Trim(v, `"`)
		v = strings.TrimPrefix(v, "W/")
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n >= 0 {
			return n
		}
		return -2 // 显式提供了但无法解析 → 交给上层拒绝
	}
	if v := r.URL.Query().Get("expected_version"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n >= 0 {
			return n
		}
		return -2
	}
	return -1
}
