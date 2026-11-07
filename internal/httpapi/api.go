// Package httpapi 是 HTTP 传输层：路由、中间件、DTO 与错误映射。
// 对外只暴露业务语义的接口（交换机 → VRF/VLAN …），不暴露原始设备
// 读写；全部业务语义在 service 层实现。
package httpapi

import (
	"log/slog"
	"net/http"

	"switchctl/internal/config"
	"switchctl/internal/service"
)

// Deps 是 HTTP 层的依赖集合。
type Deps struct {
	Config  *config.Config
	Service *service.Service
	Logger  *slog.Logger
}

// Server 持有路由与依赖。
type Server struct {
	deps Deps
	mux  *http.ServeMux
}

// New 构造 HTTP 服务。
func New(deps Deps) *Server {
	s := &Server{deps: deps, mux: http.NewServeMux()}
	s.routes()
	return s
}

// Handler 返回带完整中间件链的 http.Handler。
func (s *Server) Handler() http.Handler {
	var h http.Handler = s.mux
	h = maxBytesMiddleware(s.deps.Config.Server.MaxConfigBytes)(h)
	h = loggingMiddleware(s.deps.Logger)(h)
	h = requestIDMiddleware(h)
	return recoverMiddleware(s.deps.Logger)(h)
}

// routes 集中注册全部路由。
// 注意：Go 1.22+ ServeMux 对冲突模式会 panic，所有注册必须集中在这里，
// 保证冲突在启动时立刻暴露。
func (s *Server) routes() {
	// 交换机（业务视角的只读信息）
	s.mux.HandleFunc("GET /api/v1/switches", s.handleListSwitches)

	// 业务：VRF
	s.mux.HandleFunc("GET /api/v1/switches/{name}/vrfs", s.handleListVRFs)
	s.mux.HandleFunc("PUT /api/v1/switches/{name}/vrfs/{vrf}", s.handlePutVRF)
	s.mux.HandleFunc("DELETE /api/v1/switches/{name}/vrfs/{vrf}", s.handleDeleteVRF)

	// 业务：VLAN
	s.mux.HandleFunc("GET /api/v1/switches/{name}/vlans", s.handleListVLANs)
	s.mux.HandleFunc("PUT /api/v1/switches/{name}/vlans/{id}", s.handlePutVLAN)
	s.mux.HandleFunc("DELETE /api/v1/switches/{name}/vlans/{id}", s.handleDeleteVLAN)

	// 业务：VLAN 三层接口（SVI）
	s.mux.HandleFunc("GET /api/v1/switches/{name}/vlan-interfaces", s.handleListSVIs)
	s.mux.HandleFunc("PUT /api/v1/switches/{name}/vlan-interfaces/{vlan}", s.handlePutSVI)
	s.mux.HandleFunc("DELETE /api/v1/switches/{name}/vlan-interfaces/{vlan}", s.handleDeleteSVI)

	// 业务：SVI 与 VRF 绑定
	s.mux.HandleFunc("PUT /api/v1/switches/{name}/vlan-interfaces/{vlan}/vrf", s.handleJoinSVIVRF)
	s.mux.HandleFunc("DELETE /api/v1/switches/{name}/vlan-interfaces/{vlan}/vrf", s.handleLeaveSVIVRF)

	// 业务：VRF 内 BGP 邻居
	s.mux.HandleFunc("GET /api/v1/switches/{name}/vrfs/{vrf}/bgp-neighbors", s.handleListBGPNeighbors)
	s.mux.HandleFunc("PUT /api/v1/switches/{name}/vrfs/{vrf}/bgp-neighbors/{neighbor}", s.handlePutBGPNeighbor)
	s.mux.HandleFunc("DELETE /api/v1/switches/{name}/vrfs/{vrf}/bgp-neighbors/{neighbor}", s.handleDeleteBGPNeighbor)

	// 异步操作查询与控制
	s.mux.HandleFunc("GET /api/v1/operations", s.handleListOperations)
	s.mux.HandleFunc("GET /api/v1/operations/{id}", s.handleGetOperation)

	// 指标（Prometheus 文本格式）
	s.mux.HandleFunc("GET /metrics", s.handleMetrics)
}
