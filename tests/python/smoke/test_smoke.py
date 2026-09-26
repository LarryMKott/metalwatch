# -*- coding: utf-8 -*-
"""冒烟测试：系统核心路径快速验证（目标 < 5 分钟，实际秒级）。

通过标准：服务在跑、能登录、核心只读接口 200、本机 Agent 在线。
全部为只读或零副作用请求，可安全高频执行。
"""
import socket

import pytest

from mw import client, config


def test_01_healthz():
    """存活探针：服务端健康且双存储正常。"""
    status, body = client.call("GET", "/healthz", expect=200)
    assert body["status"] == "ok"
    assert body["storage"]["metadata"]["driver"] == "sqlite"
    assert body["storage"]["tsdb"]["driver"] == "embedded"


def test_02_login_and_me(admin_password):
    """鉴权主路径：登录 → me 返回管理员身份。"""
    token = client.login(password=admin_password)
    assert token
    status, me = client.call("GET", "/api/v1/auth/me", token=token, expect=200)
    assert me["username"] == "admin"


def test_03_unauthorized_rejected():
    """未带令牌访问管理接口必须 401（W11 生效）。"""
    status, body = client.call("GET", "/api/v1/hosts")
    assert status == 401 and body["code"] == "unauthorized"


def test_04_hosts_list(admin):
    """资产列表：分页字段完整。"""
    _, body = admin("GET", "/api/v1/hosts?page=1&page_size=5", expect=200)
    for key in ("items", "total", "page", "page_size"):
        assert key in body


def test_05_overview(admin):
    """大盘：计数与趋势序列结构完整。"""
    _, body = admin("GET", "/api/v1/overview", expect=200)
    for key in ("host_total", "host_online", "host_offline", "alert_active", "series"):
        assert key in body
    assert isinstance(body["series"], list) and body["series"]


def test_06_alerts_and_templates(admin):
    """告警中心：事件列表与内置阈值模板可查。"""
    _, alerts = admin("GET", "/api/v1/alerts?state=all", expect=200)
    assert isinstance(alerts, list)
    _, tpls = admin("GET", "/api/v1/alerts/templates", expect=200)
    assert len(tpls) >= 6, "内置阈值模板应 ≥ 6 条"


def test_07_system_status(admin):
    """系统状态：版本、存储与主机计数。"""
    _, body = admin("GET", "/api/v1/system/status", expect=200)
    assert body["storage"]["metadata_driver"] == "sqlite"
    assert body["storage"]["tsdb_driver"] == "embedded"


def test_08_storage_backends(admin):
    """存储目录：内嵌实现已标记、外接后端未实现。"""
    _, body = admin("GET", "/api/v1/system/storage/backends", expect=200)
    flags = {(b["kind"], b["name"]): b["implemented"] for b in body["backends"]}
    assert flags[("metadata", "sqlite")] is True
    assert flags[("timeseries", "embedded")] is True
    assert flags[("timeseries", "prometheus")] is False


def test_09_agent_online(admin, agent_info):
    """本机 Agent 在线。

    门禁以实际状态为准：资产列表为空（全新环境/CI）→ 跳过；
    有资产但无一在线（Agent 掉线）→ 判失败。
    """
    if agent_info is None:
        pytest.skip("本机 Agent 未部署（缺少令牌文件）")
    _, body = admin("GET", "/api/v1/hosts", expect=200)
    if body["total"] == 0:
        pytest.skip("服务端无已注册资产（全新环境）")
    online = [h for h in body["items"] if h["status"] == "online"]
    assert online, "有资产但无一在线：Agent 掉线或未运行"


def test_10_frontend_reachable():
    """前端 dev server 可达（未启动则跳过，不影响冒烟结论）。"""
    try:
        host, port = config.FRONTEND_URL.split("//")[1].split(":")
        socket.create_connection((host, int(port)), timeout=2).close()
    except OSError:
        pytest.skip("前端 dev server 未启动")
    import urllib.request
    with urllib.request.urlopen(config.FRONTEND_URL, timeout=5) as resp:
        assert resp.status == 200
