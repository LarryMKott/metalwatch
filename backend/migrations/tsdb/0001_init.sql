-- MetalWatch 内嵌时序存储（进程内嵌，默认时序后端）
--
-- 与元数据分开成独立文件（<data_dir>/tsdb.db），理由：
--   1. 元数据可切到独立部署的 MySQL / PostgreSQL，时序仍可保持内嵌，二者生命周期解耦；
--   2. 时序写入频次远高于元数据，独立文件避免与元数据事务争抢同一个写锁；
--   3. 备份策略不同：元数据小且需强一致，时序大而可容忍重建，可分别设定备份频率。
--
-- 方言约束（与 0001_init.sql 一致，便于跨库迁移）：
--   1. 时间戳统一由应用层写 UTC RFC3339；本表额外用 ms epoch 整数列做范围查询与排序
--   2. 不使用部分唯一索引（MySQL 不支持），唯一性靠 labels_hash 列的普通唯一索引
--   3. 枚举用 TEXT、布尔用 INTEGER 0/1
--   4. 占位符统一用 ?（本文件仅用于 SQLite）

-- 时间线目录。
-- series_id 由应用层计算：FNV-1a 64(metric + 序列化后的标签)，不同 tier 独立编号。
CREATE TABLE IF NOT EXISTS ts_series (
  series_id   INTEGER PRIMARY KEY,
  metric      TEXT    NOT NULL,
  labels_json TEXT    NOT NULL,
  labels_hash TEXT    NOT NULL,
  tier        INTEGER NOT NULL DEFAULT 0,          -- 0=原始档 1=5m 聚合档
  last_ts     INTEGER NOT NULL DEFAULT 0,          -- 该时间线最新采样的 ms epoch
  last_value  REAL,
  created_at  TEXT    NOT NULL
) STRICT;

CREATE INDEX IF NOT EXISTS ix_ts_series_metric ON ts_series (metric, tier);
CREATE UNIQUE INDEX IF NOT EXISTS uk_ts_series_hash ON ts_series (labels_hash, tier);

-- 数据块：一条时间线在一段时间窗口内的采样打包成一个 BLOB，按时间线 + 时间范围检索。
-- 分块而不是每点一行，是为了把行数压到「时间线数 × 块数」，
-- 让查询退化为少量 BLOB 的顺序读取（无 JOIN、无逐点索引维护）。
CREATE TABLE IF NOT EXISTS ts_chunk (
  id        INTEGER PRIMARY KEY AUTOINCREMENT,
  series_id INTEGER NOT NULL REFERENCES ts_series (series_id) ON DELETE CASCADE,
  tier      INTEGER NOT NULL DEFAULT 0,
  start_ts  INTEGER NOT NULL,                      -- 块内首点的 ms epoch
  end_ts    INTEGER NOT NULL,                      -- 块内末点的 ms epoch
  points    INTEGER NOT NULL,                      -- 块内点数，用于解码时预分配
  encoding  TEXT    NOT NULL,                      -- xor（原始档）/ agg（聚合档）
  bytes     INTEGER NOT NULL,                      -- BLOB 字节数，便于容量统计
  data      BLOB    NOT NULL
) STRICT;

CREATE INDEX IF NOT EXISTS ix_ts_chunk_lookup ON ts_chunk (series_id, tier, start_ts);
CREATE INDEX IF NOT EXISTS ix_ts_chunk_expire ON ts_chunk (tier, end_ts);

-- 写入幂等：Agent 断网续传会重放同一 batch_id，用该表去重，避免重复采样。
-- 仅保留近期窗口（由保留任务清理），不做长期审计。
CREATE TABLE IF NOT EXISTS ts_ingest_dedup (
  batch_id  TEXT    PRIMARY KEY,
  host_id   INTEGER,
  points    INTEGER NOT NULL DEFAULT 0,
  seen_at   TEXT    NOT NULL
) STRICT;

CREATE INDEX IF NOT EXISTS ix_ts_dedup_seen ON ts_ingest_dedup (seen_at);
