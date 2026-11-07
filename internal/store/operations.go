package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/oklog/ulid/v2"
)

// NewOperationID 生成可按时间排序的操作 ID（ULID，小写）。
func NewOperationID() string {
	return strings.ToLower(ulid.MustNew(ulid.Timestamp(time.Now()), rand.Reader).String())
}

const opCols = `o.id, o.device_id, d.name, o.kind, o.status, o.phase, o.source_ip,
	o.biz_meta, o.changes, o.delta_json, o.failure_code, o.failure_message,
	o.created_at, o.started_at, o.finished_at, o.duration_ms,
	o.base_version`

const opFrom = ` FROM operations o JOIN devices d ON d.id = o.device_id`

func scanOperation(row interface{ Scan(...any) error }) (*Operation, error) {
	var (
		op          Operation
		phase       sql.NullString
		sourceIP    sql.NullString
		bizMeta     sql.NullString
		changes     sql.NullString
		delta       sql.NullString
		failureCode sql.NullString
		failureMsg  sql.NullString
		createdAt   string
		startedAt   sql.NullString
		finishedAt  sql.NullString
		durationMs  sql.NullInt64
		baseVer     sql.NullInt64
	)
	if err := row.Scan(&op.ID, &op.DeviceID, &op.DeviceName, &op.Kind, &op.Status, &phase, &sourceIP,
		&bizMeta, &changes, &delta, &failureCode, &failureMsg,
		&createdAt, &startedAt, &finishedAt, &durationMs,
		&baseVer); err != nil {
		return nil, err
	}
	if baseVer.Valid {
		v := baseVer.Int64
		op.BaseVersion = &v
	}
	op.Phase, op.SourceIP = phase.String, sourceIP.String
	op.BizMeta, op.Changes, op.DeltaJSON = bizMeta.String, changes.String, delta.String
	op.FailureCode, op.FailureMessage = failureCode.String, failureMsg.String
	var err error
	if op.CreatedAt, err = ParseTime(createdAt); err != nil {
		return nil, err
	}
	if op.StartedAt, err = timePtr(startedAt); err != nil {
		return nil, err
	}
	if op.FinishedAt, err = timePtr(finishedAt); err != nil {
		return nil, err
	}
	if durationMs.Valid {
		v := durationMs.Int64
		op.DurationMs = &v
	}
	return &op, nil
}

// InsertOperation 创建操作记录。
func (s *Store) InsertOperation(ctx context.Context, op *Operation) error {
	if op.ID == "" {
		return errors.New("操作 ID 不能为空")
	}
	if op.CreatedAt.IsZero() {
		op.CreatedAt = nowTime()
	}
	_, err := s.w.ExecContext(ctx, `
		INSERT INTO operations (id, device_id, kind, status, phase, source_ip,
			biz_meta, changes, delta_json, created_at, base_version)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		op.ID, op.DeviceID, op.Kind, op.Status, nullStr(op.Phase), nullStr(op.SourceIP),
		nullStr(op.BizMeta), nullStr(op.Changes), nullStr(op.DeltaJSON),
		FormatTime(op.CreatedAt), op.BaseVersion)
	if err != nil {
		return fmt.Errorf("创建操作: %w", err)
	}
	return nil
}

// GetOperation 按 ID 取操作。
func (s *Store) GetOperation(ctx context.Context, id string) (*Operation, error) {
	row := s.r.QueryRowContext(ctx, "SELECT "+opCols+opFrom+" WHERE o.id = ?", id)
	op, err := scanOperation(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("查询操作 %s: %w", id, err)
	}
	return op, nil
}

// OperationFilter 是操作列表过滤条件。
type OperationFilter struct {
	DeviceName string
	Status     string
	Kind       string
	Limit      int
}

// ListOperations 查询操作列表（按创建时间倒序）。
func (s *Store) ListOperations(ctx context.Context, f OperationFilter) ([]*Operation, error) {
	limit := f.Limit
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	q := "SELECT " + opCols + opFrom
	var (
		where []string
		args  []any
	)
	if f.DeviceName != "" {
		where = append(where, "d.name = ?")
		args = append(args, f.DeviceName)
	}
	if f.Status != "" {
		where = append(where, "o.status = ?")
		args = append(args, f.Status)
	}
	if f.Kind != "" {
		where = append(where, "o.kind = ?")
		args = append(args, f.Kind)
	}
	if len(where) > 0 {
		q += " WHERE " + strings.Join(where, " AND ")
	}
	q += " ORDER BY o.created_at DESC LIMIT ?"
	args = append(args, limit)

	rows, err := s.r.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("查询操作列表: %w", err)
	}
	defer rows.Close()
	out := []*Operation{}
	for rows.Next() {
		op, err := scanOperation(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, op)
	}
	return out, rows.Err()
}

// UpdateOperation 更新操作的可变字段。
func (s *Store) UpdateOperation(ctx context.Context, op *Operation) error {
	_, err := s.w.ExecContext(ctx, `
		UPDATE operations SET status = ?, phase = ?, changes = ?, failure_code = ?, failure_message = ?,
			started_at = ?, finished_at = ?, duration_ms = ?,
			base_version = ?
		WHERE id = ?`,
		op.Status, nullStr(op.Phase), nullStr(op.Changes), nullStr(op.FailureCode),
		nullStr(op.FailureMessage), nullableTime(op.StartedAt), nullableTime(op.FinishedAt),
		op.DurationMs, op.BaseVersion, op.ID)
	if err != nil {
		return fmt.Errorf("更新操作 %s: %w", op.ID, err)
	}
	return nil
}

// FindActiveOperation 返回设备某类型未完成的操作（用于同设备变更串行化）。
func (s *Store) FindActiveOperation(ctx context.Context, deviceID int64, kind string) (*Operation, error) {
	row := s.r.QueryRowContext(ctx, "SELECT "+opCols+opFrom+`
		WHERE o.device_id = ? AND o.kind = ? AND o.status IN ('pending','running')
		ORDER BY o.created_at LIMIT 1`, deviceID, kind)
	op, err := scanOperation(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("查询进行中的操作: %w", err)
	}
	return op, nil
}

// ListOperationsByStatus 按状态集合查询（启动恢复用）。
func (s *Store) ListOperationsByStatus(ctx context.Context, statuses ...string) ([]*Operation, error) {
	if len(statuses) == 0 {
		return nil, nil
	}
	ph := strings.TrimSuffix(strings.Repeat("?,", len(statuses)), ",")
	args := make([]any, len(statuses))
	for i, st := range statuses {
		args[i] = st
	}
	rows, err := s.r.QueryContext(ctx,
		"SELECT "+opCols+opFrom+" WHERE o.status IN ("+ph+") ORDER BY o.created_at", args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Operation{}
	for rows.Next() {
		op, err := scanOperation(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, op)
	}
	return out, rows.Err()
}

// --- 步骤 ---

// InsertStep 记录一个执行步骤。
func (s *Store) InsertStep(ctx context.Context, opID string, seq int, name string) (int64, error) {
	res, err := s.w.ExecContext(ctx, `
		INSERT INTO operation_steps (operation_id, seq, name, status, started_at)
		VALUES (?, ?, ?, ?, ?)`,
		opID, seq, name, StepRunning, nowTS())
	if err != nil {
		return 0, fmt.Errorf("记录步骤: %w", err)
	}
	return res.LastInsertId()
}

// FinishStep 完成一个步骤（顺带计算耗时）。
func (s *Store) FinishStep(ctx context.Context, stepID int64, status, summary, errMsg string) error {
	now := nowTS()
	_, err := s.w.ExecContext(ctx, `
		UPDATE operation_steps SET status = ?, finished_at = ?,
			duration_ms = CAST((julianday(?) - julianday(started_at)) * 86400000 AS INTEGER),
			summary = ?, error_message = ? WHERE id = ?`,
		status, now, now, nullStr(summary), nullStr(errMsg), stepID)
	if err != nil {
		return fmt.Errorf("完成步骤 %d: %w", stepID, err)
	}
	return nil
}

// ListSteps 返回操作的步骤明细。
func (s *Store) ListSteps(ctx context.Context, opID string) ([]*OperationStep, error) {
	rows, err := s.r.QueryContext(ctx, `
		SELECT id, operation_id, seq, name, status, started_at, finished_at, duration_ms,
			summary, error_message
		FROM operation_steps WHERE operation_id = ? ORDER BY seq`, opID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*OperationStep{}
	for rows.Next() {
		var (
			st                    OperationStep
			startedAt, finishedAt sql.NullString
			durationMs            sql.NullInt64
			summary, errMsg       sql.NullString
		)
		if err := rows.Scan(&st.ID, &st.OperationID, &st.Seq, &st.Name, &st.Status, &startedAt,
			&finishedAt, &durationMs, &summary, &errMsg); err != nil {
			return nil, err
		}
		if st.StartedAt, err = timePtr(startedAt); err != nil {
			return nil, err
		}
		if st.FinishedAt, err = timePtr(finishedAt); err != nil {
			return nil, err
		}
		if durationMs.Valid {
			v := durationMs.Int64
			st.DurationMs = &v
		}
		st.Summary, st.ErrorMessage = summary.String, errMsg.String
		out = append(out, &st)
	}
	return out, rows.Err()
}

// --- 命令明细 ---

// AppendCommands 批量写入逐行命令与回显。
func (s *Store) AppendCommands(ctx context.Context, cmds []*OperationCommand) error {
	if len(cmds) == 0 {
		return nil
	}
	tx, err := s.w.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	stmt, err := tx.PrepareContext(ctx, `
		INSERT INTO operation_commands (operation_id, seq, direction, command, output, is_error, error_flag, ts)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, c := range cmds {
		ts := c.TS
		if ts.IsZero() {
			ts = nowTime()
		}
		if _, err := stmt.ExecContext(ctx, c.OperationID, c.Seq, c.Direction,
			nullStr(c.Command), nullStr(c.Output), boolInt(c.IsError), nullStr(c.ErrorFlag),
			FormatTime(ts)); err != nil {
			return fmt.Errorf("写入命令明细: %w", err)
		}
	}
	return tx.Commit()
}

// ListCommands 返回逐行命令明细。
func (s *Store) ListCommands(ctx context.Context, opID string) ([]*OperationCommand, error) {
	rows, err := s.r.QueryContext(ctx, `
		SELECT id, operation_id, seq, direction, command, output, is_error, error_flag, ts
		FROM operation_commands WHERE operation_id = ? ORDER BY seq, id`, opID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*OperationCommand{}
	for rows.Next() {
		var (
			c       OperationCommand
			command sql.NullString
			output  sql.NullString
			errFlag sql.NullString
			tsStr   string
			isErr   int64
		)
		if err := rows.Scan(&c.ID, &c.OperationID, &c.Seq, &c.Direction, &command, &output,
			&isErr, &errFlag, &tsStr); err != nil {
			return nil, err
		}
		c.Command, c.Output, c.ErrorFlag = command.String, output.String, errFlag.String
		c.IsError = isErr != 0
		c.TS, _ = ParseTime(tsStr)
		out = append(out, &c)
	}
	return out, rows.Err()
}

// --- 工具 ---

func nullStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
