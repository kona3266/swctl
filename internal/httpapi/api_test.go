package httpapi

import (
	"io"
	"log/slog"
	"net/http/httptest"
	"testing"

	"switchctl/internal/config"
)

// TestRoutesRegister 确认全部路由可注册（Go 1.22+ 冲突模式会在 New 里 panic）。
func TestRoutesRegister(t *testing.T) {
	_ = New(Deps{
		Config: &config.Config{},
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
}

// TestPortRouteEscapedSlash 确认物理口名里的 "/" 通过 %2F 传参时能正确匹配。
func TestPortRouteEscapedSlash(t *testing.T) {
	s := New(Deps{
		Config: &config.Config{},
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})

	cases := []struct {
		method, target, pattern string
	}{
		{"PUT", "/api/v1/switches/sw1/ports/Ethernet1%2F1",
			"PUT /api/v1/switches/{name}/ports/{port}"},
		{"PUT", "/api/v1/switches/sw1/ports/Ethernet1%2F1/sub-interfaces/100",
			"PUT /api/v1/switches/{name}/ports/{port}/sub-interfaces/{vlan}"},
		{"DELETE", "/api/v1/switches/sw1/ports/Ethernet1%2F1/sub-interfaces/100",
			"DELETE /api/v1/switches/{name}/ports/{port}/sub-interfaces/{vlan}"},
		{"PUT", "/api/v1/switches/sw1/ports/Ethernet1%2F1/sub-interfaces/100/vrf",
			"PUT /api/v1/switches/{name}/ports/{port}/sub-interfaces/{vlan}/vrf"},
		{"DELETE", "/api/v1/switches/sw1/ports/Ethernet1%2F1/sub-interfaces/100/vrf",
			"DELETE /api/v1/switches/{name}/ports/{port}/sub-interfaces/{vlan}/vrf"},
		{"GET", "/api/v1/switches/sw1/ports", "GET /api/v1/switches/{name}/ports"},
	}
	for _, tc := range cases {
		req := httptest.NewRequest(tc.method, tc.target, nil)
		if _, pattern := s.mux.Handler(req); pattern != tc.pattern {
			t.Fatalf("%s %s 匹配到 %q，期望 %q", tc.method, tc.target, pattern, tc.pattern)
		}
	}

	// 未编码的 "/" 会被当成路径分隔符，不应匹配端口路由
	req := httptest.NewRequest("PUT", "/api/v1/switches/sw1/ports/Ethernet1/1", nil)
	if _, pattern := s.mux.Handler(req); pattern == "PUT /api/v1/switches/{name}/ports/{port}" {
		t.Fatal("未编码的端口名不应匹配端口路由")
	}
}
