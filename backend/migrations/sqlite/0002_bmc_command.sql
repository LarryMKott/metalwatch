-- 0002 BMC 管控指令与操作审计（W15）。
--
-- 一张表两用：指令执行记录即操作审计（前端 /bmc/audit 直接读本表）。
-- 下发即落 pending 行，终态 success/failed 由执行结果回写；command_id 为
-- 幂等键（服务端生成的短随机 ID，与 proto BmcCommand.command_id 同值）。
--
-- 不加 FK 约束：与同期的 collect_run / report_job 一致（host 删除后审计
-- 记录保留， host_name 由查询侧 JOIN 补齐，JOIN 不上时显示为空）。

CREATE TABLE bmc_command (
  id             INTEGER PRIMARY KEY AUTOINCREMENT,
  host_id        INTEGER NOT NULL,
  command_id     TEXT    NOT NULL UNIQUE,
  cmd_type       TEXT    NOT NULL CHECK (cmd_type IN ('fan','power','identify','policy')),
  target         TEXT,
  params         TEXT,
  status         TEXT    NOT NULL DEFAULT 'pending'
                 CHECK (status IN ('pending','success','failed')),
  message        TEXT,
  observed_value REAL,
  operator       TEXT,
  created_at     TEXT    NOT NULL,
  executed_at    TEXT
);

CREATE INDEX idx_bmc_command_host_time ON bmc_command (host_id, created_at);
CREATE INDEX idx_bmc_command_time      ON bmc_command (created_at);
