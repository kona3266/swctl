package config

import (
	"fmt"
	"time"

	"gopkg.in/yaml.v3"
)

// Duration 包装 time.Duration，支持 YAML 中的 "10s"、"24h" 这类字符串写法。
type Duration time.Duration

// Std 返回标准库的 time.Duration。
func (d Duration) Std() time.Duration { return time.Duration(d) }

func (d *Duration) UnmarshalYAML(node *yaml.Node) error {
	var s string
	if err := node.Decode(&s); err != nil {
		return fmt.Errorf("时长必须是字符串（如 \"10s\"、\"24h\"），第 %d 行", node.Line)
	}
	if s == "" {
		*d = 0
		return nil
	}
	v, err := time.ParseDuration(s)
	if err != nil {
		return fmt.Errorf("无效时长 %q（第 %d 行）：%w", s, node.Line, err)
	}
	if v < 0 {
		return fmt.Errorf("时长不能为负：%q（第 %d 行）", s, node.Line)
	}
	*d = Duration(v)
	return nil
}
