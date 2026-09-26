# -*- coding: utf-8 -*-
"""单元测试 · Agent 接入协议（M4/W3 JSON 通道）：注册、上报校验、幂等、心跳。

测试数据隔离：每个用例经 SQLite 直写种子注册码，注册出的主机用后即删
（agent_token 表随主机级联删除）。
"""
import uuid

import pytest

from mw import client, config, db

def _now():
    import time
    return time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime())


def _enroll(code, hostname=None):
    return client.call("POST", "/api/v1/agent/enroll", body={
        "enroll_code": code, "hostname": hostname or ("py-agent-" + uuid.uuid4().hex[:8]),
        "primary_ip": "10.78.0.1", "smbios_uuid": "py-uuid-" + uuid.uuid4().hex[:10],
        "os_type": "linux", "agent_version": "py-test"})


@pytest.fixture()
def enrolled(admin):
    """一对 (host_id, agent_token)：种子注册码注册，用后删除主机。"""
    code = db.seed_enroll_code("MW-PY-" + uuid.uuid4().hex[:12])
    status, resp = _enroll(code)
    assert status == 201, resp
    host_id, token = resp["host_id"], resp["agent_token"]
    assert token.startswith("mwa_")
    yield host_id, token
    admin("DELETE", f"/api/v1/hosts/{host_id}")


def test_enroll_bad_code_401():
    """异常路径：无效注册码拒绝注册。"""
    status, body = _enroll("MW-NO-SUCH-CODE")
    assert status == 401 and body["code"] == "enroll_code_used"


def test_enroll_invalid_input_422():
    """输入验证：非法 IP / 缺 hostname 应 422。"""
    code = db.seed_enroll_code("MW-PY-" + uuid.uuid4().hex[:12])
    status, _ = client.call("POST", "/api/v1/agent/enroll", body={
        "enroll_code": code, "hostname": "x", "primary_ip": "bad-ip"})
    assert status == 422


def test_enroll_once_then_reuse_denied(admin):
    """一次性语义：注册成功后同一注册码再注册必须 401。"""
    code = db.seed_enroll_code("MW-PY-" + uuid.uuid4().hex[:12])
    status, first = _enroll(code)
    assert status == 201
    try:
        status, body = _enroll(code, hostname="second-host")
        assert status == 401
    finally:
        admin("DELETE", f"/api/v1/hosts/{first['host_id']}")


def test_report_requires_token(enrolled):
    """边界：无令牌上报 401。"""
    host_id, _ = enrolled
    status, _ = client.call("POST", "/api/v1/agent/report", body={
        "batch_id": "b1", "host_id": host_id, "metrics": []})
    assert status == 401


def test_report_forbidden_scope(enrolled):
    """越权：host_id 与令牌绑定主机不一致必须 403（M4 验收）。"""
    host_id, token = enrolled
    status, body = client.call("POST", "/api/v1/agent/report", token=token, body={
        "batch_id": "b1", "host_id": host_id + 999, "metrics": []})
    assert status == 403 and body["code"] == "forbidden_scope"


def test_report_metric_whitelist(enrolled):
    """白名单：未知指标 422 metric_not_allowed。"""
    host_id, token = enrolled
    status, body = client.call("POST", "/api/v1/agent/report", token=token, body={
        "batch_id": "b1", "host_id": host_id,
        "collected_at": _now(),
        "metrics": [{"name": "cpu_usage_percent", "value": 10}]})
    assert status == 422 and body["code"] == "metric_not_allowed"


def test_report_high_cardinality_label(enrolled):
    """高基数防护：sn 标签必须被拒（docs/03 §3）。"""
    host_id, token = enrolled
    status, body = client.call("POST", "/api/v1/agent/report", token=token, body={
        "batch_id": "b1", "host_id": host_id, "collected_at": _now(),
        "metrics": [{"name": "cpu_temp_celsius", "labels": {"sn": "CN123"}, "value": 40}]})
    assert status == 422 and body["code"] == "metric_not_allowed"


def test_report_backfill_window(enrolled):
    """补传窗口：collected_at 早于 24h 拒收 422。"""
    host_id, token = enrolled
    status, body = client.call("POST", "/api/v1/agent/report", token=token, body={
        "batch_id": "b1", "host_id": host_id,
        "collected_at": "2020-01-01T00:00:00Z",
        "metrics": [{"name": "cpu_temp_celsius", "value": 40}]})
    assert status == 422 and body["code"] == "timestamp_out_of_range"


def test_report_requires_batch_id(enrolled):
    """幂等键：缺 batch_id 应 422。"""
    host_id, token = enrolled
    status, _ = client.call("POST", "/api/v1/agent/report", token=token, body={
        "host_id": host_id, "metrics": []})
    assert status == 422


def test_report_accept_and_replay_dedup(enrolled):
    """幂等（M3 验收 2）：合法上报 202；同 batch_id 重放 accepted=0 且标记 deduplicated。"""
    host_id, token = enrolled
    payload = {
        "batch_id": "py-" + uuid.uuid4().hex[:12], "host_id": host_id,
        "collected_at": _now(),
        "metrics": [{"name": "cpu_temp_celsius", "labels": {"chip": "CPU"}, "value": 41.5}],
    }
    status, first = client.call("POST", "/api/v1/agent/report", token=token,
                                body=payload, expect=202)
    assert first["accepted"] == 1 and first["tsdb"] == "accepted"
    status, replay = client.call("POST", "/api/v1/agent/report", token=token,
                                 body=payload, expect=202)
    assert replay["accepted"] == 0 and replay["tsdb"] == "deduplicated"


def test_heartbeat_ok(enrolled):
    """心跳：无指标轻量上报返回 200 与服务端口径周期。"""
    host_id, token = enrolled
    status, body = client.call("POST", "/api/v1/agent/heartbeat", token=token, body={
        "host_id": host_id, "agent_version": "py-test", "uptime_sec": 1})
    assert status == 200 and body["ok"] is True
    assert body["report_interval_sec"] > 0


def test_enroll_empty_uuid_creates_host(admin):
    """边界：无 smbios_uuid 注册成功但每次新建主机（无可复用指纹）。"""
    # 两次注册消耗两个用量：空指纹不做主机复用
    code = db.seed_enroll_code("MW-PY-" + uuid.uuid4().hex[:12], uses=2)
    created = []
    try:
        for i in range(2):
            status, resp = _enroll(code, hostname=f"py-nouuid-{i}")
            assert status == 201
            created.append(resp["host_id"])
        assert created[0] != created[1], "空指纹不应复用主机"
    finally:
        for hid in created:
            admin("DELETE", f"/api/v1/hosts/{hid}")


def test_report_empty_metrics_accepted(enrolled):
    """边界：空指标列表合法上报，accepted=0 且在线状态照常刷新。"""
    host_id, token = enrolled
    status, resp = client.call("POST", "/api/v1/agent/report", token=token, body={
        "batch_id": "py-empty-" + uuid.uuid4().hex[:8], "host_id": host_id,
        "collected_at": _now(), "metrics": []}, expect=202)
    assert resp["accepted"] == 0


def test_report_protobuf_content_type_501(enrolled):
    """协议演进：protobuf 通道尚未启用，必须 501 而非误解析。"""
    import urllib.request, json as _json
    host_id, token = enrolled
    req = urllib.request.Request(config.BASE_URL + "/api/v1/agent/report",
                                 data=_json.dumps({"batch_id": "p", "host_id": host_id}).encode(),
                                 method="POST")
    req.add_header("Content-Type", "application/x-protobuf")
    req.add_header("Authorization", "Bearer " + token)
    try:
        with urllib.request.urlopen(req, timeout=10) as resp:
            status = resp.status
    except client.urllib.error.HTTPError as e:
        status = e.code
    assert status == 501


def test_report_wrong_method_404(enrolled):
    """边界：GET 上报端点不存在（gin 方法树隔离）。"""
    import urllib.request
    host_id, token = enrolled
    req = urllib.request.Request(config.BASE_URL + "/api/v1/agent/report",
                                 headers={"Authorization": "Bearer " + token})
    try:
        with urllib.request.urlopen(req, timeout=10) as resp:
            status = resp.status
    except client.urllib.error.HTTPError as e:
        status = e.code
    assert status == 404


def test_report_invalid_json_4xx(enrolled):
    """异常处理：非法 JSON 体应 4xx，不得 500。"""
    import urllib.request
    host_id, token = enrolled
    req = urllib.request.Request(config.BASE_URL + "/api/v1/agent/report",
                                 data=b"{not-json", method="POST")
    req.add_header("Content-Type", "application/json")
    req.add_header("Authorization", "Bearer " + token)
    try:
        with urllib.request.urlopen(req, timeout=10) as resp:
            status = resp.status
    except client.urllib.error.HTTPError as e:
        status = e.code
    assert 400 <= status < 500
