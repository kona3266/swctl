-- 002_cleanup.sql — 移除旧架构遗留字段。
--
-- 001 保持 v2 初始形态，本迁移同时适用于全新库与已有 v2 库。

DROP INDEX IF EXISTS idx_operations_running_per_device;

-- 设备清单只需要清单字段；探测信息与启用位从未被使用。
ALTER TABLE devices DROP COLUMN enabled;
ALTER TABLE devices DROP COLUMN capabilities;
