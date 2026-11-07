-- 004_atomic_apply.sql — 去掉控制器侧回滚：操作只记录"基于哪个版本"，不再记录回滚目标。
--
-- 语义变化：一个北向操作必须在**一次原子提交**内完成。设备侧要么整体生效、要么整体不生效，
-- 因此控制器不再需要"回滚到变更前"的补偿机制，也不需要优先队列。
--
-- 原 rollback_target_version 表示"回滚目标版本"，现在只表示"本操作基于的版本"
-- （目标版本 = base_version + 1），改名以反映语义。

ALTER TABLE operations RENAME COLUMN rollback_target_version TO base_version;
