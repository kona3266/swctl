// Package driver 定义执行层与设备之间的抽象边界（southbound seam）。
//
// 接口以**厂商无关模型**为交换物，不含任何平台语法假设：
// 拉取（Pull）返回解析后的模型与原始快照，应用（Apply）接收模型差异。
// 平台差异——连接方式（NETCONF / NX-API / …）、命令或 XML 渲染、错误判定、
// 配置归一化、标记编码——全部由各后端实现承担。
//
// 目前仓库内尚无后端实现（SSH CLI 会话引擎已移除）：
//   - cisco_nxos  → NX-API（DME 模型树）或 NETCONF
//   - ruijie_rgos → NETCONF（RGOS 12.5 起支持，需 netconf capability candidate）
package driver

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"time"

	"switchctl/internal/model"
)

// ErrNotConnected 表示尚未建立会话。
var ErrNotConnected = errors.New("设备会话未建立")

// Device 是执行层使用的设备抽象。
// 实现必须自行处理：连接/认证、配置拉取与解析、差异渲染与下发、错误判定、
// 配置持久化（各平台语义不同：commit / copy run start / write memory）。
type Device interface {
	// Open 建立会话并完成能力探测。
	Open(ctx context.Context) error
	// Close 释放连接。
	Close() error
	// Pull 拉取设备当前配置：返回解析后的模型与原始快照。
	Pull(ctx context.Context) (*Snapshot, error)
	// Apply 把模型差异**在一次原子提交内**应用到设备：整批要么全部生效、要么全部不生效。
	//
	// 这是控制器的硬性约定（控制器不做失败回滚）：
	//   - NETCONF：写 candidate 后 <commit>；
	//   - NX-API：一次 configure session 的 commit，或一次 DME 批量请求；
	//   - 任何"逐条下发、失败留半套"的实现都违反本接口契约。
	Apply(ctx context.Context, d *model.Diff) (*ApplyResult, error)
	// Commit 把改动持久化到设备的启动配置。
	Commit(ctx context.Context) error
}

// Snapshot 是一次设备配置抓取的产物。
type Snapshot struct {
	// Format 是原始快照的格式标识，由实现定义（如 netconf-xml、nxapi-json、cli-text）。
	Format string `json:"format"`
	// Raw 是原始快照内容，用于人工比对与调试。
	Raw []byte `json:"-"`
	// Digest 是归一化后的摘要（十六进制），用于判断"设备配置是否变化"。
	// 归一化规则（剥离时间戳等易变内容）由实现负责。
	Digest string `json:"digest"`
	// LineCount 是快照行数；结构化格式可为 0。
	LineCount int `json:"line_count"`
	// Config 是解析后的模型（仅业务管理域）。
	Config *model.DeviceConfig `json:"-"`
}

// ChangeResult 是单个变更单元的执行结果（单元的含义由实现决定：
// 一条命令、一个 XML 节点、一次 RPC……）。
type ChangeResult struct {
	Item      string `json:"item"`
	Output    string `json:"output,omitempty"`
	IsError   bool   `json:"is_error,omitempty"`
	ErrorFlag string `json:"error_flag,omitempty"`
}

// ApplyResult 是一次下发的汇总。
type ApplyResult struct {
	Changes    []ChangeResult `json:"changes"`
	Failed     bool           `json:"failed"`
	ErrorFlag  string         `json:"error_flag,omitempty"`
	ErrorText  string         `json:"error_text,omitempty"`
	FailedItem string         `json:"failed_item,omitempty"`
}

// Config 是构造后端实现的通用参数（不含任何平台特有的连接细节）。
type Config struct {
	// Name 是设备在清单中的名字。
	Name string
	// Endpoint 是连接地址（host:port），端口语义由平台决定。
	Endpoint string
	// Username / Password / PrivateKeyPEM / PrivateKeyPassphrase 是认证材料。
	Username             string
	Password             string
	PrivateKeyPEM        []byte
	PrivateKeyPassphrase string
	// OperationTimeout 是单个操作的总时限。
	OperationTimeout time.Duration
	// Marker 是写入设备、标识"归控制器管理"的业务标记值；
	// 具体编码方式（description 字段、名称后缀……）由实现决定。
	Marker string
	// Logger 是带设备上下文的日志器。
	Logger *slog.Logger
}

// IsTransient 判断错误是否可能由设备尚未就绪/网络暂不可用引起。
// 认证失败、能力不支持等属于确定性错误，不应重试。
func IsTransient(err error) bool {
	if err == nil {
		return false
	}
	var netErr net.Error
	return errors.As(err, &netErr) ||
		errors.Is(err, context.DeadlineExceeded) ||
		errors.Is(err, ErrNotConnected)
}
