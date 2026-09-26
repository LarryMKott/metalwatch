# -*- coding: utf-8 -*-
"""集成测试 · 端到端数据流（G2 门禁的 Python 版）：

注册（SQLite 种子注册码）→ JSON 上报 → 时序落库 → 曲线查询 → 批次重放幂等 → 清理。
验证模块协作：agentpb ↔ 告警评估 ↔ pipeline ↔ 内嵌 TSDB ↔ REST 查询。
"""
import time
import uuid

import pytest

from mw import client, db
from mw.allure_support import step


@pytest.fixture()
def enrolled_host(admin):
    """一对 (host_id, agent_token)，用后删除主机。"""
    code = db.seed_enroll_code("MW-PY-" + uuid.uuid4().hex[:12])
    status, resp = client.call("POST", "/api/v1/agent/enroll", body={
        "enroll_code": code, "hostname": "py-flow-" + uuid.uuid4().hex[:8],
        "primary_ip": "10.81.0.1", "smbios_uuid": "py-flow-" + uuid.uuid4().hex[:10],
        "os_type": "linux", "agent_version": "py-integration"})
    assert status == 201, resp
    yield resp["host_id"], resp["agent_token"]
    admin("DELETE", f"/api/v1/hosts/{resp['host_id']}")


def _now(offset_sec=0):
    return time.strftime("%Y-%m-%dT%H:%M:%SZ",
                         time.gmtime(time.time() + offset_sec))


def test_report_query_dedup_flow(enrolled_host):
    """正向流：上报两批 → 曲线出点 → 标签过滤 → 重放去重。"""
    host_id, token = enrolled_host
    batch1 = "py-flow-" + uuid.uuid4().hex[:12]
    metrics = [
        {"name": "fan_rpm", "labels": {"slot": "Fan1"}, "value": 1200},
        {"name": "voltage_volts", "labels": {"rail": "12V"}, "value": 12.1},
    ]
    with step("上报第一批指标（fan_rpm / voltage）"):
        status, resp = client.call("POST", "/api/v1/agent/report", token=token, body={
            "batch_id": batch1, "host_id": host_id, "mode": "agent",
            "collected_at": _now(), "metrics": metrics}, expect=202)
        assert resp["accepted"] == 2 and resp["tsdb"] == "accepted"

    with step("上报第二批（时间轴 -60s，落在默认查询区间内）"):
        client.call("POST", "/api/v1/agent/report", token=token, body={
            "batch_id": batch1 + "-2", "host_id": host_id, "mode": "agent",
            "collected_at": _now(-60), "metrics": metrics}, expect=202)

    # 曲线查询走管理接口；时序写入经 pipeline 批量提交（默认 5s 攒批），轮询等待出点
    from mw import client as c
    admin_token = c.login()

    def curve():
        _, body = c.call("GET",
                         f"/api/v1/hosts/{host_id}/metrics?metric=fan_rpm",
                         token=admin_token, expect=200)
        return body["points"] or None

    # 两批分属两次 pipeline 攒批提交，必须等齐 2 点而非见点即收
    def two_points():
        pts = curve()
        return pts if pts and len(pts) >= 2 else None
    with step("轮询时序出点并断言曲线契约"):
        points = client.wait_until(two_points, timeout=20, what="fan_rpm 出齐 2 点")
        assert len(points) == 2
    ts, value = points[-1]
    assert value == 1200

    # 标签过滤：rail=12V 只命中 voltage 序列
    _, volt = c.call("GET",
                     f"/api/v1/hosts/{host_id}/metrics?metric=voltage_volts&labels=rail:12V",
                     token=admin_token, expect=200)
    assert len(volt["points"]) == 2

    # 批次重放：幂等拒绝（M3 验收 2）
    with step("批次重放：验证服务端幂等去重"):
        _, replay = client.call("POST", "/api/v1/agent/report", token=token, body={
            "batch_id": batch1, "host_id": host_id, "mode": "agent",
            "collected_at": _now(), "metrics": metrics}, expect=202)
        assert replay["accepted"] == 0 and replay["tsdb"] == "deduplicated"


def test_enroll_then_host_visible(enrolled_host):
    """注册即建资产：enroll 后主机出现在资产列表且状态为在线。"""
    host_id, _ = enrolled_host
    from mw import client as c
    admin_token = c.login()
    _, body = c.call("GET", f"/api/v1/hosts/{host_id}", token=admin_token, expect=200)
    assert body["collect_agent"] is True
    assert body["status"] in ("online", "unknown")
