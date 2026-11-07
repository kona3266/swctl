-- 001_init.sql — switchctl v2 初始 schema（权威配置模型的 desired-state 架构）
--
-- 说明：项目未投产，v1 的迁移（tasks/desired_config/checkpoints 等）已整体废弃，
-- 此文件是 v2 的初始 schema。后续 schema 清理见 002_cleanup.sql。

-- 设备清单（YAML 同步）
CREATE TABLE devices (
  id           INTEGER PRIMARY KEY AUTOINCREMENT,
  name         TEXT    NOT NULL UNIQUE,
  host         TEXT    NOT NULL,
  port         INTEGER NOT NULL DEFAULT 22,
  dialect      TEXT    NOT NULL CHECK (dialect IN ('cisco_ios','cisco_nxos','ruijie_rgos')),
  enabled      INTEGER NOT NULL DEFAULT 1,
  capabilities TEXT,           -- 探测信息 JSON（版本/主机名）
  last_seen_at TEXT,
  last_error   TEXT,
  created_at   TEXT    NOT NULL,
  updated_at   TEXT    NOT NULL
);

-- 权威配置模型：每设备一份全量模型文档（业务管理域：VRF/VLAN）
CREATE TABLE device_configs (
  device_id          INTEGER PRIMARY KEY REFERENCES devices(id),
  model_json         TEXT    NOT NULL,
  version            INTEGER NOT NULL DEFAULT 0,
  sync_state         TEXT    NOT NULL DEFAULT 'unknown'
                             CHECK (sync_state IN ('unknown','synced','drifted')),
  updated_at         TEXT    NOT NULL,
  last_reconciled_at TEXT
);

-- 模型版本历史（审计；为后续"回滚到某版本"留路）
CREATE TABLE device_config_versions (
  id           INTEGER PRIMARY KEY AUTOINCREMENT,
  device_id    INTEGER NOT NULL REFERENCES devices(id),
  version      INTEGER NOT NULL,
  model_json   TEXT    NOT NULL,
  operation_id TEXT,
  created_at   TEXT    NOT NULL,
  UNIQUE (device_id, version)
);

-- 操作（异步）：快路径推送 / 对账
CREATE TABLE operations (
  id              TEXT PRIMARY KEY,          -- ULID
  device_id       INTEGER NOT NULL REFERENCES devices(id),
  kind            TEXT    NOT NULL CHECK (kind IN ('apply','reconcile')),
  status          TEXT    NOT NULL
                          CHECK (status IN ('pending','running','succeeded','no_change','failed','interrupted')),
  phase           TEXT,
  source_ip       TEXT,
  biz_meta        TEXT,                      -- 业务元数据 JSON（resource/name/action）
  changes         TEXT,                      -- 变更摘要 JSON（快路径）或收敛差异摘要（对账）
  delta_json      TEXT,                      -- 本次变更的抽象差异（模型 delta，快路径提交时留存；命令在执行时渲染）
  failure_code    TEXT,
  failure_message TEXT,
  created_at      TEXT    NOT NULL,
  started_at      TEXT,
  finished_at     TEXT,
  duration_ms     INTEGER
);

CREATE INDEX idx_operations_device_created ON operations(device_id, created_at DESC);
CREATE INDEX idx_operations_status ON operations(status);
CREATE UNIQUE INDEX idx_operations_running_per_device ON operations(device_id) WHERE status = 'running';

CREATE TABLE operation_steps (
  id            INTEGER PRIMARY KEY AUTOINCREMENT,
  operation_id  TEXT    NOT NULL REFERENCES operations(id) ON DELETE CASCADE,
  seq           INTEGER NOT NULL,
  name          TEXT    NOT NULL,
  status        TEXT    NOT NULL CHECK (status IN ('pending','running','ok','failed','skipped')),
  started_at    TEXT,
  finished_at   TEXT,
  duration_ms   INTEGER,
  summary       TEXT,
  error_message TEXT,
  UNIQUE (operation_id, seq)
);

-- 逐行命令与设备回显（审计/排障）
CREATE TABLE operation_commands (
  id           INTEGER PRIMARY KEY AUTOINCREMENT,
  operation_id TEXT    NOT NULL REFERENCES operations(id) ON DELETE CASCADE,
  seq          INTEGER NOT NULL,
  direction    TEXT    NOT NULL CHECK (direction IN ('send','recv')),
  command      TEXT,
  output       TEXT,
  is_error     INTEGER NOT NULL DEFAULT 0,
  error_flag   TEXT,
  ts           TEXT    NOT NULL
);

CREATE INDEX idx_operation_commands_op ON operation_commands(operation_id, seq);

