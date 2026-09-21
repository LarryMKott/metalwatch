-- MetalWatch 初始化迁移（SQLite 方言）
-- 迁移编号 0001；同一编号在其他方言目录下有对应文件，schema_version 记录已应用版本。
--
-- 可移植性约定（与 postgres/0001_init.sql 保持一致）：
--   1. 时间戳一律由应用层写入 UTC RFC3339 文本，DDL 的 DEFAULT 仅作兜底
--   2. 布尔语义用 INTEGER/SMALLINT 0/1，由 Go 层映射 bool
--   3. 枚举用 TEXT + CHECK 表达，应用层做二次校验
--   4. 不使用部分唯一索引（MySQL 不支持）——「可空列的唯一性」用普通 UNIQUE 实现
--      （SQLite / PostgreSQL / MySQL 都允许多个 NULL 并存），告警去重改用 active_key 列
--   5. JSON 统一存 TEXT，结构合法性由应用层保证（SQLite 侧追加 json_valid 约束）

PRAGMA foreign_keys = ON;

-- ============================ 资产域 ============================

CREATE TABLE host (
  id              INTEGER PRIMARY KEY AUTOINCREMENT,
  hostname        TEXT    NOT NULL,
  primary_ip      TEXT    NOT NULL,
  bmc_ip          TEXT    UNIQUE,
  sn              TEXT,
  smbios_uuid     TEXT    UNIQUE,
  site            TEXT,
  rack            TEXT,
  rack_unit       INTEGER,
  os_type         TEXT    NOT NULL DEFAULT 'unknown' CHECK (os_type IN ('linux','windows','unknown')),
  os_version      TEXT,
  collect_agent   INTEGER NOT NULL DEFAULT 1,
  collect_ipmi    INTEGER NOT NULL DEFAULT 0,
  agent_version   TEXT,
  status          TEXT    NOT NULL DEFAULT 'unknown' CHECK (status IN ('online','offline','unknown')),
  last_seen_at    TEXT,
  geo_country     TEXT,
  geo_region      TEXT,
  geo_city        TEXT,
  geo_asn         INTEGER,
  geo_asn_org     TEXT,
  geo_updated_at  TEXT,
  remark          TEXT,
  created_at      TEXT    NOT NULL,
  updated_at      TEXT    NOT NULL
) STRICT;

CREATE UNIQUE INDEX uk_host_smbios_uuid ON host (smbios_uuid);
CREATE UNIQUE INDEX uk_host_bmc_ip      ON host (bmc_ip);
CREATE INDEX idx_host_hostname    ON host (hostname);
CREATE INDEX idx_host_primary_ip  ON host (primary_ip);
CREATE INDEX idx_host_sn          ON host (sn);
CREATE INDEX idx_host_status_seen ON host (status, last_seen_at);
CREATE INDEX idx_host_geo         ON host (geo_country, geo_region);


CREATE TABLE host_component (
  id              INTEGER PRIMARY KEY AUTOINCREMENT,
  host_id         INTEGER NOT NULL REFERENCES host(id) ON DELETE CASCADE,
  category        TEXT    NOT NULL CHECK (category IN
                    ('cpu','memory','disk','nic','raid_controller','psu','fan','gpu',
                     'mainboard','bios','bmc_fw','chassis','other')),
  slot            TEXT    NOT NULL DEFAULT '',
  name            TEXT    NOT NULL DEFAULT '',
  vendor          TEXT,
  serial          TEXT,
  firmware        TEXT,
  capacity_bytes  INTEGER,
  media_type      TEXT,
  health          TEXT,
  extra           TEXT,
  first_seen_at   TEXT    NOT NULL,
  last_seen_at    TEXT    NOT NULL,
  removed_at      TEXT,
  UNIQUE (host_id, category, slot)
) STRICT;

CREATE INDEX idx_component_host_cat ON host_component (host_id, category, removed_at);
CREATE INDEX idx_component_serial   ON host_component (serial);


CREATE TABLE hardware_snapshot (
  id           INTEGER PRIMARY KEY AUTOINCREMENT,
  host_id      INTEGER NOT NULL REFERENCES host(id) ON DELETE CASCADE,
  collect_mode TEXT    NOT NULL CHECK (collect_mode IN ('agent','ipmi')),
  captured_at  TEXT    NOT NULL,
  fingerprint  TEXT    NOT NULL,
  payload      TEXT
) STRICT;

CREATE INDEX idx_snapshot_host_time ON hardware_snapshot (host_id, captured_at);


CREATE TABLE change_event (
  id             INTEGER PRIMARY KEY AUTOINCREMENT,
  host_id        INTEGER NOT NULL REFERENCES host(id) ON DELETE CASCADE,
  snapshot_id    INTEGER REFERENCES hardware_snapshot(id) ON DELETE SET NULL,
  category       TEXT    NOT NULL,
  slot           TEXT    NOT NULL DEFAULT '',
  change_type    TEXT    NOT NULL CHECK (change_type IN ('added','removed','modified')),
  field          TEXT,
  old_value      TEXT,
  new_value      TEXT,
  detected_at    TEXT    NOT NULL,
  confirm_count  INTEGER NOT NULL DEFAULT 1,
  alert_event_id INTEGER
) STRICT;

CREATE INDEX idx_change_host_time ON change_event (host_id, detected_at);
CREATE INDEX idx_change_type_time ON change_event (change_type, detected_at);


CREATE TABLE bmc_credential (
  id            INTEGER PRIMARY KEY AUTOINCREMENT,
  host_id       INTEGER NOT NULL UNIQUE REFERENCES host(id) ON DELETE CASCADE,
  username      TEXT    NOT NULL,
  secret_cipher BLOB    NOT NULL,
  secret_nonce  BLOB    NOT NULL,
  key_version   INTEGER NOT NULL DEFAULT 1,
  protocol      TEXT    NOT NULL DEFAULT 'ipmi20' CHECK (protocol IN ('ipmi15','ipmi20','redfish')),
  updated_at    TEXT    NOT NULL
) STRICT;


CREATE TABLE tag (
  id         INTEGER PRIMARY KEY AUTOINCREMENT,
  name       TEXT    NOT NULL UNIQUE,
  color      TEXT    NOT NULL DEFAULT '#64748B',
  kind       TEXT    NOT NULL DEFAULT 'manual' CHECK (kind IN ('manual','geo','auto')),
  created_at TEXT    NOT NULL
) STRICT;


CREATE TABLE host_tag (
  host_id INTEGER NOT NULL REFERENCES host(id) ON DELETE CASCADE,
  tag_id  INTEGER NOT NULL REFERENCES tag(id)  ON DELETE CASCADE,
  PRIMARY KEY (host_id, tag_id)
) STRICT;

CREATE INDEX idx_host_tag_tag ON host_tag (tag_id);


-- ============================ 接入与采集域 ============================

CREATE TABLE agent_token (
  id           INTEGER PRIMARY KEY AUTOINCREMENT,
  host_id      INTEGER REFERENCES host(id) ON DELETE CASCADE,
  token_hash   TEXT    NOT NULL UNIQUE,
  remark       TEXT,
  state        TEXT    NOT NULL DEFAULT 'active' CHECK (state IN ('active','revoked')),
  expire_at    TEXT,
  enrolled_at  TEXT,
  last_used_at TEXT,
  created_at   TEXT    NOT NULL
) STRICT;


CREATE TABLE enroll_code (
  id         INTEGER PRIMARY KEY AUTOINCREMENT,
  code_hash  TEXT    NOT NULL UNIQUE,
  expire_at  TEXT    NOT NULL,
  max_uses   INTEGER NOT NULL DEFAULT 1,
  used_count INTEGER NOT NULL DEFAULT 0,
  created_by TEXT,
  created_at TEXT    NOT NULL
) STRICT;


CREATE TABLE collect_task (
  id           INTEGER PRIMARY KEY AUTOINCREMENT,
  name         TEXT    NOT NULL UNIQUE,
  kind         TEXT    NOT NULL CHECK (kind IN
                 ('agent_push','ipmi_poll','asset_snapshot','smart_poll','report')),
  cron_expr    TEXT,
  interval_sec INTEGER,
  timeout_sec  INTEGER NOT NULL DEFAULT 30,
  retry_max    INTEGER NOT NULL DEFAULT 3,
  concurrency  INTEGER,
  params       TEXT,
  enabled      INTEGER NOT NULL DEFAULT 1,
  created_at   TEXT    NOT NULL,
  updated_at   TEXT    NOT NULL
) STRICT;


CREATE TABLE collect_run (
  id          INTEGER PRIMARY KEY AUTOINCREMENT,
  task_id     INTEGER,
  host_id     INTEGER,
  mode        TEXT    NOT NULL CHECK (mode IN ('agent','ipmi')),
  state       TEXT    NOT NULL CHECK (state IN ('ok','fail','timeout','skip','degraded')),
  error_code  TEXT,
  error_msg   TEXT,
  duration_ms INTEGER,
  started_at  TEXT    NOT NULL,
  finished_at TEXT
) STRICT;

CREATE INDEX idx_run_host_time  ON collect_run (host_id, started_at);
CREATE INDEX idx_run_state_time ON collect_run (state, started_at);


-- ============================ 告警域 ============================

CREATE TABLE threshold_template (
  id           INTEGER PRIMARY KEY AUTOINCREMENT,
  name         TEXT    NOT NULL,
  metric       TEXT    NOT NULL,
  category     TEXT    NOT NULL CHECK (category IN
                 ('sensor','smart','raid','psu','fan','agent','asset')),
  op           TEXT    NOT NULL DEFAULT 'gt' CHECK (op IN ('gt','lt','eq','ne')),
  warn_value   REAL,
  crit_value   REAL,
  duration_sec INTEGER NOT NULL DEFAULT 120,
  enabled      INTEGER NOT NULL DEFAULT 1,
  builtin      INTEGER NOT NULL DEFAULT 0,
  UNIQUE (metric, name)
) STRICT;

CREATE INDEX idx_tpl_category ON threshold_template (category);


CREATE TABLE threshold_override (
  id           INTEGER PRIMARY KEY AUTOINCREMENT,
  host_id      INTEGER NOT NULL REFERENCES host(id) ON DELETE CASCADE,
  metric       TEXT    NOT NULL,
  op           TEXT    NOT NULL DEFAULT 'gt' CHECK (op IN ('gt','lt','eq','ne')),
  warn_value   REAL,
  crit_value   REAL,
  duration_sec INTEGER,
  enabled      INTEGER NOT NULL DEFAULT 1,
  updated_by   TEXT,
  updated_at   TEXT    NOT NULL,
  UNIQUE (host_id, metric)
) STRICT;


-- 去重机制：active_key 在 firing 时写入（host:category:metric:object），
-- resolved/silenced 时置 NULL。唯一索引对 NULL 不生效，从而既保证「同一故障只开一条」，
-- 又能保留完整故障史，且不依赖部分唯一索引（跨方言可移植）。
CREATE TABLE alert_event (
  id            INTEGER PRIMARY KEY AUTOINCREMENT,
  host_id       INTEGER REFERENCES host(id) ON DELETE SET NULL,
  severity      TEXT    NOT NULL CHECK (severity IN ('critical','major','info')),
  category      TEXT    NOT NULL CHECK (category IN
                  ('sensor','smart','raid','psu','fan','asset_change','agent_offline',
                   'collect_failed','system')),
  metric        TEXT,
  object_name   TEXT,
  value         REAL,
  threshold     REAL,
  title         TEXT    NOT NULL,
  detail        TEXT,
  state         TEXT    NOT NULL DEFAULT 'firing'
                  CHECK (state IN ('firing','resolved','silenced','suppressed')),
  active_key    TEXT,
  first_seen_at TEXT    NOT NULL,
  last_seen_at  TEXT    NOT NULL,
  resolved_at   TEXT,
  ack_by        TEXT,
  ack_at        TEXT,
  notify_state  TEXT    NOT NULL DEFAULT 'pending'
                  CHECK (notify_state IN ('pending','sent','failed','skipped')),
  notified_at   TEXT
) STRICT;

CREATE UNIQUE INDEX uk_alert_active_key ON alert_event (active_key);
CREATE INDEX idx_alert_host_state ON alert_event (host_id, state, last_seen_at);
CREATE INDEX idx_alert_sev_state  ON alert_event (severity, state);
CREATE INDEX idx_alert_time       ON alert_event (first_seen_at);


CREATE TABLE notify_channel (
  id            INTEGER PRIMARY KEY AUTOINCREMENT,
  name          TEXT    NOT NULL UNIQUE,
  type          TEXT    NOT NULL CHECK (type IN ('webhook','email','syslog')),
  config        TEXT    NOT NULL CHECK (json_valid(config)),
  min_severity  TEXT    NOT NULL DEFAULT 'major' CHECK (min_severity IN ('critical','major','info')),
  enabled       INTEGER NOT NULL DEFAULT 1,
  last_result   TEXT,
  last_tried_at TEXT,
  created_at    TEXT    NOT NULL
) STRICT;


CREATE TABLE alert_silence (
  id         INTEGER PRIMARY KEY AUTOINCREMENT,
  host_id    INTEGER REFERENCES host(id) ON DELETE CASCADE,
  tag_id     INTEGER REFERENCES tag(id) ON DELETE CASCADE,
  metric     TEXT,
  starts_at  TEXT    NOT NULL,
  ends_at    TEXT    NOT NULL,
  reason     TEXT,
  created_by TEXT
) STRICT;

CREATE INDEX idx_silence_window ON alert_silence (starts_at, ends_at);


CREATE TABLE maintenance_window (
  id           INTEGER PRIMARY KEY AUTOINCREMENT,
  name         TEXT    NOT NULL,
  host_ids     TEXT,
  starts_at    TEXT    NOT NULL,
  ends_at      TEXT    NOT NULL,
  alert_policy TEXT    NOT NULL DEFAULT 'silence'
                 CHECK (alert_policy IN ('silence','info_downgrade')),
  created_by   TEXT
) STRICT;

CREATE INDEX idx_mw_window ON maintenance_window (starts_at, ends_at);


-- ============================ 用户与运维域 ============================

CREATE TABLE app_user (
  id            INTEGER PRIMARY KEY AUTOINCREMENT,
  username      TEXT    NOT NULL UNIQUE,
  display_name  TEXT,
  password_hash TEXT    NOT NULL,
  role          TEXT    NOT NULL DEFAULT 'viewer'
                  CHECK (role IN ('admin','operator','viewer')),
  state         TEXT    NOT NULL DEFAULT 'active' CHECK (state IN ('active','disabled')),
  last_login_at TEXT,
  last_login_ip TEXT,
  created_at    TEXT    NOT NULL
) STRICT;


CREATE TABLE api_token (
  id           INTEGER PRIMARY KEY AUTOINCREMENT,
  name         TEXT    NOT NULL,
  token_hash   TEXT    NOT NULL UNIQUE,
  scopes       TEXT    NOT NULL DEFAULT 'asset:read,metric:read',
  state        TEXT    NOT NULL DEFAULT 'active' CHECK (state IN ('active','revoked')),
  expire_at    TEXT,
  last_used_at TEXT,
  created_by   TEXT,
  created_at   TEXT    NOT NULL
) STRICT;


CREATE TABLE audit_log (
  id          INTEGER PRIMARY KEY AUTOINCREMENT,
  user_id     INTEGER,
  username    TEXT,
  action      TEXT    NOT NULL,
  target_type TEXT,
  target_id   TEXT,
  source_ip   TEXT,
  result      TEXT    NOT NULL DEFAULT 'ok' CHECK (result IN ('ok','denied','fail')),
  detail      TEXT,
  created_at  TEXT    NOT NULL
) STRICT;

CREATE INDEX idx_audit_user_time   ON audit_log (user_id, created_at);
CREATE INDEX idx_audit_action_time ON audit_log (action, created_at);


CREATE TABLE report_job (
  id          INTEGER PRIMARY KEY AUTOINCREMENT,
  kind        TEXT    NOT NULL CHECK (kind IN
                ('asset_list','inspection','alert_history','change_history')),
  format      TEXT    NOT NULL CHECK (format IN ('json','xlsx','csv','html')),
  params      TEXT,
  state       TEXT    NOT NULL DEFAULT 'queued'
                CHECK (state IN ('queued','running','done','failed','expired')),
  file_path   TEXT,
  file_size   INTEGER,
  expire_at   TEXT,
  error_msg   TEXT,
  created_by  TEXT,
  created_at  TEXT    NOT NULL,
  finished_at TEXT
) STRICT;

CREATE INDEX idx_report_state_time ON report_job (state, created_at);


CREATE TABLE app_setting (
  k          TEXT PRIMARY KEY,
  v          TEXT,
  updated_at TEXT NOT NULL
) STRICT;
