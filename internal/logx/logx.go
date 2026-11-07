// Package logx 提供 slog 初始化与脱敏：任何字符串属性在写出前都会按
// 配置的正则逐行检查，命中行整体替换为 [REDACTED]，避免设备配置中的
// 密码/密钥类文本进入日志。
package logx

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"regexp"
	"strings"
)

// New 创建日志器。level: debug|info|warn|error；format: text|json。
// redactPatterns 为逐行脱敏正则。
func New(level, format string, w io.Writer, redactPatterns []string) (*slog.Logger, error) {
	if w == nil {
		w = os.Stderr
	}
	lvl, err := parseLevel(level)
	if err != nil {
		return nil, err
	}
	opts := &slog.HandlerOptions{Level: lvl}

	var inner slog.Handler
	switch format {
	case "json":
		inner = slog.NewJSONHandler(w, opts)
	case "text":
		inner = slog.NewTextHandler(w, opts)
	default:
		return nil, fmt.Errorf("未知日志格式 %q", format)
	}

	pats := make([]*regexp.Regexp, 0, len(redactPatterns))
	for i, p := range redactPatterns {
		re, err := regexp.Compile(p)
		if err != nil {
			return nil, fmt.Errorf("脱敏正则 [%d] 非法: %w", i, err)
		}
		pats = append(pats, re)
	}
	return slog.New(&redactHandler{inner: inner, patterns: pats}), nil
}

func parseLevel(s string) (slog.Level, error) {
	switch strings.ToLower(s) {
	case "debug":
		return slog.LevelDebug, nil
	case "info":
		return slog.LevelInfo, nil
	case "warn":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	default:
		return 0, fmt.Errorf("未知日志级别 %q", s)
	}
}

// redactString 对字符串逐行应用脱敏规则。
func redactString(s string, patterns []*regexp.Regexp) string {
	if len(patterns) == 0 || s == "" {
		return s
	}
	lines := strings.Split(s, "\n")
	changed := false
	for i, line := range lines {
		for _, re := range patterns {
			if re.MatchString(line) {
				lines[i] = "[REDACTED]"
				changed = true
				break
			}
		}
	}
	if !changed {
		return s
	}
	return strings.Join(lines, "\n")
}

type redactHandler struct {
	inner    slog.Handler
	patterns []*regexp.Regexp
}

func (h *redactHandler) Enabled(ctx context.Context, l slog.Level) bool {
	return h.inner.Enabled(ctx, l)
}

func (h *redactHandler) Handle(ctx context.Context, r slog.Record) error {
	if len(h.patterns) == 0 {
		return h.inner.Handle(ctx, r)
	}
	return h.inner.Handle(ctx, redactRecord(r, h.patterns))
}

func (h *redactHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &redactHandler{inner: h.inner.WithAttrs(attrs), patterns: h.patterns}
}

func (h *redactHandler) WithGroup(name string) slog.Handler {
	return &redactHandler{inner: h.inner.WithGroup(name), patterns: h.patterns}
}

func redactRecord(r slog.Record, patterns []*regexp.Regexp) slog.Record {
	out := slog.NewRecord(r.Time, r.Level, redactString(r.Message, patterns), r.PC)
	r.Attrs(func(a slog.Attr) bool {
		out.AddAttrs(redactAttr(a, patterns))
		return true
	})
	return out
}

func redactAttr(a slog.Attr, patterns []*regexp.Regexp) slog.Attr {
	switch a.Value.Kind() {
	case slog.KindString:
		return slog.String(a.Key, redactString(a.Value.String(), patterns))
	case slog.KindGroup:
		g := a.Value.Group()
		out := make([]slog.Attr, len(g))
		for i, sub := range g {
			out[i] = redactAttr(sub, patterns)
		}
		return slog.Attr{Key: a.Key, Value: slog.GroupValue(out...)}
	default:
		return a
	}
}
