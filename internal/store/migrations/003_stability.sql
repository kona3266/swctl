-- 003_stability.sql — 稳定性增强：verified 状态分离。
--
-- 设计要点：status 保持粗粒度（pending/running/succeeded/failed/…）。

-- 设备配置：最后一次"已验证落地"的版本（desired 与 verified 分离）。
-- desired_version 即 device_configs.version；verified_version 只在收敛验证通过后推进。
ALTER TABLE device_configs ADD COLUMN verified_version INTEGER NOT NULL DEFAULT 0;
ALTER TABLE device_configs ADD COLUMN verified_at TEXT;

-- rollback_target_version：本操作基于的版本（004 迁移中改名为 base_version）。
ALTER TABLE operations ADD COLUMN rollback_target_version INTEGER;
