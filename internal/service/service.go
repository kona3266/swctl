// Package service 是应用层：业务操作在权威模型（内存 + DB）上提交，
// 然后把抽象变更交给编排器异步推送。HTTP 层只做协议转换。
package service

import (
	"log/slog"
	"sync"

	"switchctl/internal/config"
	"switchctl/internal/orchestrator"
	"switchctl/internal/state"
	"switchctl/internal/store"
)

// Service 提供服务级操作。
type Service struct {
	cfg    *config.Config
	store  *store.Store
	state  *state.Manager
	runner *orchestrator.Runner
	log    *slog.Logger

	// 单实例部署下，每设备一把锁把"读快照 → 改模型 → 提交"变成原子操作，
	// 防止并发提交互相覆盖（设备执行队列只保证推送顺序，不保护权威态）。
	lockMu      sync.Mutex
	deviceLocks map[int64]*sync.Mutex
}

// New 构造服务。
func New(cfg *config.Config, st *store.Store, sm *state.Manager, runner *orchestrator.Runner, log *slog.Logger) *Service {
	return &Service{
		cfg: cfg, store: st, state: sm, runner: runner, log: log,
		deviceLocks: map[int64]*sync.Mutex{},
	}
}

// lockDevice 锁定某台设备的权威态提交，返回解锁函数。
func (s *Service) lockDevice(deviceID int64) func() {
	s.lockMu.Lock()
	mu := s.deviceLocks[deviceID]
	if mu == nil {
		mu = &sync.Mutex{}
		s.deviceLocks[deviceID] = mu
	}
	s.lockMu.Unlock()
	mu.Lock()
	return mu.Unlock
}

// Error 是服务层的结构化错误（HTTP 层据此映射状态码）。
type Error struct {
	Kind string // not_found | invalid | not_managed | conflict | busy | unsupported | unavailable
	Msg  string
}

func (e *Error) Error() string { return e.Msg }

func errNotFound(msg string) *Error { return &Error{Kind: "not_found", Msg: msg} }
func errInvalid(msg string) *Error  { return &Error{Kind: "invalid", Msg: msg} }
func errUnsupported(msg string) *Error {
	return &Error{Kind: "unsupported", Msg: msg}
}
func errNotManaged(msg string) *Error { return &Error{Kind: "not_managed", Msg: msg} }

// errConflict 表示并发写冲突（基于过期版本提交）。
func errConflict(msg string) *Error { return &Error{Kind: "conflict", Msg: msg} }
