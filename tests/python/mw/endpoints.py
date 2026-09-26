# -*- coding: utf-8 -*-
"""端点覆盖率登记：服务端 REST 端点清单 + 命中统计。

ENDPOINTS 是「已实现端点」的单一事实来源（与 backend/api/web/router.go 同步维护）。
新增后端路由时须同步登记，覆盖率统计才有意义。
"""
import json
import re
import threading
from pathlib import Path

# (method, path-template) 集合。模板参数统一写作 :id。
ENDPOINTS = {
    # 系统 / 存储
    ("GET", "/healthz"),
    ("GET", "/api/v1/system/status"),
    ("GET", "/api/v1/system/storage/backends"),
    ("GET", "/api/v1/overview"),
    # 资产
    ("GET", "/api/v1/hosts"),
    ("POST", "/api/v1/hosts"),
    ("GET", "/api/v1/hosts/:id"),
    ("DELETE", "/api/v1/hosts/:id"),
    ("GET", "/api/v1/hosts/:id/metrics"),
    ("GET", "/api/v1/hosts/:id/components"),
    ("GET", "/api/v1/hosts/:id/changes"),
    # 告警
    ("GET", "/api/v1/alerts"),
    ("GET", "/api/v1/alerts/templates"),
    ("POST", "/api/v1/alerts/:id/ack"),
    # 带外（W4）
    ("PUT", "/api/v1/hosts/:id/bmc"),
    ("DELETE", "/api/v1/hosts/:id/bmc"),
    ("POST", "/api/v1/hosts/:id/bmc/test"),
    ("GET", "/api/v1/collect-runs"),
    # 鉴权（W11）
    ("POST", "/api/v1/auth/login"),
    ("DELETE", "/api/v1/auth/logout"),
    ("GET", "/api/v1/auth/me"),
    ("POST", "/api/v1/auth/change-password"),
    ("GET", "/api/v1/api-tokens"),
    ("POST", "/api/v1/api-tokens"),
    ("DELETE", "/api/v1/api-tokens/:id"),
    ("GET", "/api/v1/audit-logs"),
    # 实时推送（W7）
    ("GET", "/api/v1/ws/alerts"),
    # Agent 通道
    ("POST", "/api/v1/agent/enroll"),
    ("POST", "/api/v1/agent/report"),
    ("POST", "/api/v1/agent/heartbeat"),
}

_LOCK = threading.Lock()
_HITS = set()


def normalize(path: str) -> str:
    """把实际请求路径归一为端点模板：连续数字段替换为 :id。"""
    return re.sub(r"/\d+(?=/|$)", "/:id", path)


def record(method: str, path: str) -> None:
    """登记一次端点命中（由 client.call 自动调用）。"""
    key = (method.upper(), normalize(path))
    with _LOCK:
        _HITS.add(key)


def hits() -> set:
    with _LOCK:
        return set(_HITS)


def dump(path: Path) -> None:
    """把命中集合合并写入 JSON 文件（供 run_tests.py 汇总）。"""
    with _LOCK:
        data = sorted(f"{m} {p}" for m, p in _HITS)
    path = Path(path)
    prev = []
    if path.exists():
        try:
            prev = json.loads(path.read_text(encoding="utf-8"))
        except (ValueError, OSError):
            prev = []
    merged = sorted(set(prev) | set(data))
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(json.dumps(merged, indent=1), encoding="utf-8")


def summary(hits_file: Path) -> dict:
    """统计端点覆盖率：covered / total / percent / missing。"""
    covered = set()
    if Path(hits_file).exists():
        covered = set(json.loads(Path(hits_file).read_text(encoding="utf-8")))
    total = len(ENDPOINTS)
    hit_keys = set()
    for item in covered:
        m, _, p = item.partition(" ")
        if (m, p) in ENDPOINTS:
            hit_keys.add((m, p))
    missing = sorted(f"{m} {p}" for m, p in ENDPOINTS - hit_keys)
    return {
        "covered": len(hit_keys),
        "total": total,
        "percent": round(100.0 * len(hit_keys) / total, 1) if total else 0.0,
        "missing": missing,
    }
