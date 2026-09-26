# -*- coding: utf-8 -*-
"""集成测试 · 告警闭环（W7 全链路）：

阈值触发（critical）→ 同步落库 → WebSocket 推送 → Webhook 投递（HMAC 签名）
→ 确认 ack → 三周期恢复 → resolved 通知。

使用独立注册主机（每次运行唯一），告警去重键互不干扰，可重复执行。
"""
import hmac
import hashlib
import json
import time
import uuid

import pytest

from mw import client, db, hook, ws
from mw.allure_support import step

# 内置 CPU 温度模板阈值（warn 75 / crit 85，duration 120s）
CRIT = 85.0


@pytest.fixture(scope="module")
def receiver():
    """模块级 webhook 接收器。"""
    recv = hook.WebhookReceiver().start()
    yield recv
    recv.stop()


@pytest.fixture()
def channel(receiver):
    """种子一个 webhook 渠道，用后删除。"""
    name = "py-hook-" + uuid.uuid4().hex[:8]
    db.seed_webhook(name, f"http://127.0.0.1:{receiver.port}/hook",
                    secret="py-test-secret", min_severity="info")
    yield name
    db.delete_webhook(name)


@pytest.fixture()
def victim(admin):
    """一台独立主机（唯一主机名 → 告警去重键唯一），用后删除。"""
    code = db.seed_enroll_code("MW-PY-" + uuid.uuid4().hex[:12])
    hostname = "py-alert-" + uuid.uuid4().hex[:8]
    status, resp = client.call("POST", "/api/v1/agent/enroll", body={
        "enroll_code": code, "hostname": hostname,
        "primary_ip": "10.82.0.1", "smbios_uuid": "py-alert-" + uuid.uuid4().hex[:10],
        "os_type": "linux", "agent_version": "py-integration"})
    assert status == 201
    # 注意：EnrollResponse 不含 hostname，需用注册前构造的值
    yield resp["host_id"], resp["agent_token"], hostname
    admin("DELETE", f"/api/v1/hosts/{resp['host_id']}")


def _report(host_id, token, batch, temp, offset_min):
    client.call("POST", "/api/v1/agent/report", token=token, body={
        "batch_id": batch, "host_id": host_id, "mode": "agent",
        "collected_at": time.strftime(
            "%Y-%m-%dT%H:%M:%SZ", time.gmtime(time.time() + offset_min * 60)),
        "metrics": [{"name": "cpu_temp_celsius", "labels": {"chip": "CPU"}, "value": temp}],
    }, expect=202)


def _ws_event(conn, want, timeout=12):
    """读取 WS 帧直到匹配目标事件（容忍无关帧），超时抛 AssertionError。"""
    deadline = time.time() + timeout
    while time.time() < deadline:
        try:
            frame = conn.recv_json(timeout=max(1, deadline - time.time()))
        except (TimeoutError, ConnectionError):
            break
        if frame.get("event") == want:
            return frame
    raise AssertionError(f"未收到 WS 事件 {want}")


def _capture_event(receiver, want, timeout=15):
    """轮询 webhook 捕获直到出现目标事件。"""
    def found():
        return next((r for r in receiver.capture() if r["event"] == want), None)
    return client.wait_until(found, timeout=timeout, what=f"webhook {want}")


def test_alert_full_cycle(admin, victim, channel, receiver, admin_token):
    """告警全生命周期：触发 → 推送/投递 → 确认 → 恢复。"""
    host_id, token, hostname = victim
    conn = ws.WSClient("127.0.0.1", 18080, "/api/v1/ws/alerts",
                       headers={"Authorization": "Bearer " + admin_token})
    try:
        # batch_id 必须每次运行唯一：服务端按 batch 幂等去重，重放批次
        # 会以 202/accepted=0 短路（不评估告警、不更新状态）
        b1, b2 = f"py-a1-{uuid.uuid4().hex[:8]}", f"py-a2-{uuid.uuid4().hex[:8]}"
        with step("两拍越界（回填时间轴满足 120s 持续判定）"):
            _report(host_id, token, b1, 92, -3)
            _report(host_id, token, b2, 92, 0)

        # 3) 落库断言：firing + critical
        def firing():
            _, alerts = admin("GET", f"/api/v1/alerts?state=active&host_id={host_id}",
                              expect=200)
            hit = [a for a in alerts if a["severity"] == "critical"]
            return hit or None
        with step("落库断言：firing + critical"):
            hit = client.wait_until(firing, timeout=10, what="critical firing 告警")
            alert_id = hit[0]["id"]

        # 4) WebSocket 实时推送
        with step("WebSocket 实时推送"):
            frame = _ws_event(conn, "alert.firing")
        assert frame["severity"] == "critical"
        assert frame["alert"]["metric"] == "cpu_temp_celsius"
        assert frame["host"]["hostname"] == hostname

        # 5) Webhook 投递 + HMAC 签名验证（docs/04 §5）
        with step("Webhook 投递 + HMAC 签名验证"):
            rec = _capture_event(receiver, "alert.firing")
        payload = json.loads(rec["body"])
        assert payload["alert"]["metric"] == "cpu_temp_celsius"
        expect_sig = hmac.new(b"py-test-secret", rec["body"].encode(),
                              hashlib.sha256).hexdigest()
        assert rec["sig"] == expect_sig, "HMAC-SHA256 签名不符"
        assert db.alert_notify_state(alert_id) == "sent"

        # 6) 确认 ack
        with step("确认 ack"):
            admin("POST", f"/api/v1/alerts/{alert_id}/ack", expect=200)
        _, acked = admin("GET", f"/api/v1/alerts?state=acked&host_id={host_id}",
                         expect=200)
        assert any(a["id"] == alert_id for a in acked)

        # 7) 三周期恢复（引擎 recoverN=3）
        with step("三周期恢复（引擎 recoverN=3）"):
            for i in (1, 2, 3):
                _report(host_id, token, f"py-r{i}-{uuid.uuid4().hex[:8]}", 60, i)

        def resolved_frame():
            try:
                return _ws_event(conn, "alert.resolved", timeout=5)
            except AssertionError:
                return None
        with step("恢复通知（WS + Webhook）与故障史断言"):
            frame2 = client.wait_until(resolved_frame, timeout=20, what="WS alert.resolved")
            assert frame2["host"]["hostname"] == hostname
            _capture_event(receiver, "alert.resolved")

        # 8) 故障史保留：resolved 状态可查
        _, hist = admin("GET", f"/api/v1/alerts?state=resolved&host_id={host_id}",
                        expect=200)
        assert any(a["id"] == alert_id for a in hist)
    finally:
        conn.close()
