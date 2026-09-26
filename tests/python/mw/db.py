"""SQLite 直读/直写：种子数据（注册码、通知渠道）与落库断言（notify_state、密文检查）。

独立短连接，避免与服务端（WAL + 单写者）争锁；写操作仅用于测试种子与清理。
"""
import hashlib
import json
import sqlite3
import time

from . import config


def _connect():
    conn = sqlite3.connect(str(config.DB_PATH), timeout=10)
    conn.row_factory = sqlite3.Row
    return conn


def query(sql, args=()):
    with _connect() as conn:
        return [dict(r) for r in conn.execute(sql, args).fetchall()]


def execute(sql, args=()):
    with _connect() as conn:
        conn.execute(sql, args)
        conn.commit()


def admin_password():
    """从引导管理员文件解析初始口令。"""
    text = config.BOOTSTRAP_FILE.read_text(encoding="utf-8")
    for line in text.splitlines():
        if line.startswith("password:"):
            return line.split(":", 1)[1].strip()
    raise RuntimeError(f"引导口令文件格式不符: {config.BOOTSTRAP_FILE}")


# ---------- 种子 ----------

def seed_enroll_code(code, days=1, uses=1):
    """登记一个一次性注册码（服务端校验的是 SHA-256 摘要）。"""
    code_hash = hashlib.sha256(code.encode()).hexdigest()
    expire = time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime(time.time() + days * 86400))
    now = time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime())
    execute(
        "INSERT INTO enroll_code (code_hash, expire_at, max_uses, used_count, created_by, created_at)"
        " VALUES (?, ?, ?, 0, 'py-tests', ?)",
        (code_hash, expire, uses, now))
    return code


def seed_webhook(name, url, secret, min_severity="info"):
    """登记一个启用的 webhook 通知渠道。"""
    now = time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime())
    execute(
        "INSERT INTO notify_channel (name, type, config, min_severity, enabled, created_at)"
        " VALUES (?, 'webhook', ?, ?, 1, ?)"
        " ON CONFLICT(name) DO UPDATE SET config = excluded.config, enabled = 1",
        (name, json.dumps({"url": url, "secret": secret}), min_severity, now))


def delete_webhook(name):
    execute("DELETE FROM notify_channel WHERE name = ?", (name,))


# ---------- 断言辅助 ----------

def host_by_hostname(hostname):
    rows = query("SELECT * FROM host WHERE hostname = ?", (hostname,))
    return rows[0] if rows else None


def alerts_for_host(host_id, category=None, state=None):
    sql = "SELECT * FROM alert_event WHERE host_id = ?"
    args = [host_id]
    if category:
        sql += " AND category = ?"
        args.append(category)
    if state:
        sql += " AND state = ?"
        args.append(state)
    return query(sql + " ORDER BY id", args)


def alert_notify_state(alert_id):
    rows = query("SELECT notify_state FROM alert_event WHERE id = ?", (alert_id,))
    return rows[0]["notify_state"] if rows else None


def bmc_credential(host_id):
    rows = query("SELECT * FROM bmc_credential WHERE host_id = ?", (host_id,))
    return rows[0] if rows else None
