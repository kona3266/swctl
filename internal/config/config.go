// Package config 负责 switchctl 的 YAML 配置加载、默认值填充与合法性校验。
package config

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Config 是服务的全部配置。
type Config struct {
	Server    Server    `yaml:"server"`
	Storage   Storage   `yaml:"storage"`
	Execution Execution `yaml:"execution"`
	Marking   Marking   `yaml:"marking"`
	Logging   Logging   `yaml:"logging"`
	Devices   []Device  `yaml:"devices"`
}

type Server struct {
	Listen         string   `yaml:"listen"`
	ReadTimeout    Duration `yaml:"read_timeout"`
	WriteTimeout   Duration `yaml:"write_timeout"`
	ShutdownGrace  Duration `yaml:"shutdown_grace"`
	MaxConfigBytes int64    `yaml:"max_config_bytes"`
}

type Storage struct {
	DataDir    string `yaml:"data_dir"`
	SQLitePath string `yaml:"sqlite_path"`
}

type Execution struct {
	// OperationTimeout 单个操作（应用/对账）的总时限。
	OperationTimeout Duration `yaml:"operation_timeout"`
	// MaxQueueDepth 是每设备的待执行操作队列上限；满了直接拒绝（503）而不是阻塞请求。
	MaxQueueDepth int `yaml:"max_queue_depth"`
}

// Marking 是"归控制器管理"的业务标记值。
// 该值如何写进设备（description 字段、名称后缀……）是各平台后端自己的事，
// 配置里不出现任何平台相关的编码参数。
type Marking struct {
	Value string `yaml:"value"`
}

type Logging struct {
	Level          string   `yaml:"level"`  // debug | info | warn | error
	Format         string   `yaml:"format"` // text | json
	File           string   `yaml:"file"`   // 为空则写 stderr
	RedactPatterns []string `yaml:"redact_patterns"`
}

// Device 是设备清单中的一台交换机。
type Device struct {
	Name        string      `yaml:"name"`
	Host        string      `yaml:"host"`
	Port        int         `yaml:"port"`
	Dialect     string      `yaml:"dialect"`
	Credentials Credentials `yaml:"credentials"`
	// BGPASN 本地 BGP AS 号；设备尚未运行 BGP 时，创建/删除邻居需要它
	// （设备已有 `router bgp` 时会自动解析使用）。
	BGPASN             string   `yaml:"bgp_asn"`
	ExtraErrorPatterns []string `yaml:"extra_error_patterns"`
}

type Credentials struct {
	UsernameEnv              string `yaml:"username_env"`
	UsernameFile             string `yaml:"username_file"`
	PasswordEnv              string `yaml:"password_env"`
	PasswordFile             string `yaml:"password_file"`
	EnablePasswordEnv        string `yaml:"enable_password_env"`
	EnablePasswordFile       string `yaml:"enable_password_file"`
	PrivateKeyFile           string `yaml:"private_key_file"`
	PrivateKeyPassphraseEnv  string `yaml:"private_key_passphrase_env"`
	PrivateKeyPassphraseFile string `yaml:"private_key_passphrase_file"`
}

// Load 读取并校验配置文件：解析 → 填默认值 → 校验。
func Load(path string) (*Config, error) {
	raw, err := os.ReadFile(path) // #nosec G304 -- 路径来自管理员显式指定的启动参数
	if err != nil {
		return nil, fmt.Errorf("读取配置文件: %w", err)
	}
	var cfg Config
	dec := yaml.NewDecoder(strings.NewReader(string(raw)))
	dec.KnownFields(true) // 拒绝未知字段，避免拼写错误被静默忽略
	if err := dec.Decode(&cfg); err != nil {
		return nil, fmt.Errorf("解析配置文件 %s: %w", path, err)
	}
	cfg.applyDefaults()
	if err := cfg.validate(); err != nil {
		return nil, fmt.Errorf("配置校验失败: %w", err)
	}
	return &cfg, nil
}

func (c *Config) applyDefaults() {
	s := &c.Server
	if s.Listen == "" {
		s.Listen = "127.0.0.1:8080"
	}
	if s.ReadTimeout == 0 {
		s.ReadTimeout = Duration(30 * time.Second)
	}
	if s.WriteTimeout == 0 {
		s.WriteTimeout = Duration(60 * time.Second)
	}
	if s.ShutdownGrace == 0 {
		s.ShutdownGrace = Duration(30 * time.Second)
	}
	if s.MaxConfigBytes == 0 {
		s.MaxConfigBytes = 1 << 20
	}

	st := &c.Storage
	if st.DataDir == "" {
		st.DataDir = "./data"
	}
	if st.SQLitePath == "" {
		st.SQLitePath = st.DataDir + "/switchctl.db"
	}

	e := &c.Execution
	if e.OperationTimeout == 0 {
		e.OperationTimeout = Duration(10 * time.Minute)
	}
	if e.MaxQueueDepth <= 0 {
		e.MaxQueueDepth = 128
	}

	if c.Marking.Value == "" {
		c.Marking.Value = "managed-by-switchctl"
	}

	lg := &c.Logging
	if lg.Level == "" {
		lg.Level = "info"
	}
	if lg.Format == "" {
		lg.Format = "text"
	}
	if len(lg.RedactPatterns) == 0 {
		lg.RedactPatterns = DefaultRedactPatterns()
	}

	for i := range c.Devices {
		d := &c.Devices[i]
		if d.Port == 0 {
			d.Port = 22
		}
	}
}

// validate 校验配置合法性（调用前已填默认值）。
func (c *Config) validate() error {
	var errs []error

	if c.Server.MaxConfigBytes <= 0 {
		errs = append(errs, errors.New("server.max_config_bytes 必须大于 0"))
	}

	if !oneOf(c.Logging.Level, "debug", "info", "warn", "error") {
		errs = append(errs, fmt.Errorf("logging.level 非法: %q", c.Logging.Level))
	}
	if !oneOf(c.Logging.Format, "text", "json") {
		errs = append(errs, fmt.Errorf("logging.format 非法: %q", c.Logging.Format))
	}

	if len(c.Devices) == 0 {
		errs = append(errs, errors.New("至少需要配置一台设备（devices）"))
	}
	names := map[string]bool{}
	for i := range c.Devices {
		d := &c.Devices[i]
		prefix := fmt.Sprintf("devices[%d](%s)", i, d.Name)
		if d.Name == "" {
			errs = append(errs, fmt.Errorf("devices[%d].name 不能为空", i))
		} else if names[d.Name] {
			errs = append(errs, fmt.Errorf("设备名重复: %q", d.Name))
		}
		names[d.Name] = true
		if d.Host == "" {
			errs = append(errs, fmt.Errorf("%s.host 不能为空", prefix))
		}
		if d.Dialect == "" {
			errs = append(errs, fmt.Errorf("%s.dialect 不能为空", prefix))
		}
		if d.BGPASN != "" {
			if asn, err := strconv.ParseUint(d.BGPASN, 10, 32); err != nil || asn == 0 {
				errs = append(errs, fmt.Errorf("%s.bgp_asn 非法：需要 1~4294967295 的整数", prefix))
			}
		}
		if err := d.Credentials.validate(prefix); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func oneOf(v string, opts ...string) bool {
	for _, o := range opts {
		if v == o {
			return true
		}
	}
	return false
}

// DeviceByName 按名称查找设备。
func (c *Config) DeviceByName(name string) (*Device, bool) {
	for i := range c.Devices {
		if c.Devices[i].Name == name {
			return &c.Devices[i], true
		}
	}
	return nil, false
}

// DefaultRedactPatterns 返回日志脱敏的默认规则。
func DefaultRedactPatterns() []string {
	return []string{
		`(?i)^\s*(enable|password|secret|key-string)\b.*`,
		`(?i)^\s*username\s+\S+\s+(password|secret)\b.*`,
		`(?i)^\s*snmp-server\s+community\b.*`,
		`(?i)^\s*(tacacs-server|radius-server)\s+key\b.*`,
	}
}
