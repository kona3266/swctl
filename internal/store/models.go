package store

import "time"

// 操作状态（operations.status）。
const (
	StatusPending     = "pending"
	StatusRunning     = "running"
	StatusSucceeded   = "succeeded"
	StatusNoChange    = "no_change"
	StatusFailed      = "failed"
	StatusInterrupted = "interrupted"
)

// 操作类型（operations.kind）。
const (
	KindApply     = "apply"     // 快路径：推送业务变更的命令
	KindReconcile = "reconcile" // 对账：全量收敛
)

// 同步状态（device_configs.sync_state）。
const (
	SyncUnknown = "unknown" // 从未对账
	SyncSynced  = "synced"  // 上次对账确认一致（快路径成功为乐观标记）
	SyncDrifted = "drifted" // 期望态已提交，设备未跟上
)

// 步骤状态（operation_steps.status）。
const (
	StepPending = "pending"
	StepRunning = "running"
	StepOK      = "ok"
	StepFailed  = "failed"
	StepSkipped = "skipped"
)

// Device 对应 devices 表一行。
type Device struct {
	ID         int64
	Name       string
	Host       string
	Port       int
	Dialect    string
	LastSeenAt *time.Time
	LastError  string
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

// ConfigEntry 对应 device_configs 表一行（权威模型）。
type ConfigEntry struct {
	DeviceID  int64
	ModelJSON string
	Version   int64 // desired：最后一次提交的版本
	SyncState string
	UpdatedAt time.Time
	// VerifiedVersion 是最后一次"收敛验证通过"的版本；desired > verified 即存在未落地变更。
	VerifiedVersion  int64
	VerifiedAt       *time.Time
	LastReconciledAt *time.Time
}

// Operation 对应 operations 表一行。
type Operation struct {
	ID             string
	DeviceID       int64
	DeviceName     string // 查询时 JOIN devices 得到，非存储列
	Kind           string
	Status         string
	Phase          string
	SourceIP       string
	BizMeta        string // 业务元数据 JSON
	Changes        string // 变更摘要 JSON
	DeltaJSON      string // 变更的抽象差异 JSON（模型 delta）
	FailureCode    string
	FailureMessage string
	// BaseVersion 是本操作基于的版本：本操作的目标版本 = BaseVersion + 1。
	BaseVersion *int64
	CreatedAt   time.Time
	StartedAt   *time.Time
	FinishedAt  *time.Time
	DurationMs  *int64
}

// OperationStep 对应 operation_steps 表一行。
type OperationStep struct {
	ID           int64      `json:"id"`
	OperationID  string     `json:"operation_id"`
	Seq          int        `json:"seq"`
	Name         string     `json:"name"`
	Status       string     `json:"status"`
	StartedAt    *time.Time `json:"started_at,omitempty"`
	FinishedAt   *time.Time `json:"finished_at,omitempty"`
	DurationMs   *int64     `json:"duration_ms,omitempty"`
	Summary      string     `json:"summary,omitempty"`
	ErrorMessage string     `json:"error_message,omitempty"`
}

// OperationCommand 对应 operation_commands 表一行。
type OperationCommand struct {
	ID          int64     `json:"id"`
	OperationID string    `json:"operation_id"`
	Seq         int       `json:"seq"`
	Direction   string    `json:"direction"` // send | recv
	Command     string    `json:"command,omitempty"`
	Output      string    `json:"output,omitempty"`
	IsError     bool      `json:"is_error,omitempty"`
	ErrorFlag   string    `json:"error_flag,omitempty"`
	TS          time.Time `json:"ts"`
}
