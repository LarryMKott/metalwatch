"""单元测试 · 带外管理接口（W4）：凭据录入校验、加密落库断言、连通性测试。

D9 硬性要求断言：密码明文不得出现在 bmc_credential 密文字段。
连通性测试在本机（无 ipmitool / 无 BMC）预期失败——验证的是
「凭据已解密、任务池已调度、错误如实返回」的链路。
"""
import shutil
import uuid

import pytest
from mw import db

UNIQUE = lambda: uuid.uuid4().hex[:10]


@pytest.fixture()
def host(admin):
    """一台临时主机，用后删除（级联清凭据）。"""
    _, h, _ = _mk_host(admin)
    yield h["id"]
    admin("DELETE", f"/api/v1/hosts/{h['id']}")


def _mk_host(admin):
    body = {"hostname": "py-bmc-" + UNIQUE(), "primary_ip": "10.79.0.1"}
    status, resp = admin("POST", "/api/v1/hosts", body=body)
    assert status == 201
    return status, resp, body


def test_put_bmc_input_validation(admin, host):
    """输入验证：非法 IP / 缺密码 / 非法协议均 422。"""
    for bad in (
        {"bmc_ip": "not-an-ip", "username": "u", "password": "p"},
        {"bmc_ip": "10.80.0.9", "username": "u"},
        {"bmc_ip": "10.80.0.9", "username": "u", "password": "p", "protocol": "http"},
    ):
        status, _ = admin("PUT", f"/api/v1/hosts/{host}/bmc", body=bad)
        assert status == 422, f"{bad} 应 422, got {status}"


def test_credential_encrypted_at_rest(admin, host):
    """D9 验收：密码明文不出现在密文字段；密文随 AAD 绑定不可跨主机使用（示意）。"""
    secret = "S3cret-" + UNIQUE()
    _status, _ = admin("PUT", f"/api/v1/hosts/{host}/bmc", body={
        "bmc_ip": "10.80.0.9", "username": "admin", "password": secret}, expect=204)
    cred = db.bmc_credential(host)
    assert cred is not None
    assert cred["secret_cipher"] and cred["secret_nonce"]
    assert secret.encode() not in cred["secret_cipher"]
    assert b"password" not in cred["secret_cipher"].lower()


def test_bmc_test_unreachable(admin, host):
    """异常场景：本机无 BMC，连通性测试应明确失败（502）而非 500。"""
    admin("PUT", f"/api/v1/hosts/{host}/bmc", body={
        "bmc_ip": "10.80.0.9", "username": "admin", "password": "x"}, expect=204)
    status, body = admin("POST", f"/api/v1/hosts/{host}/bmc/test")
    assert status == 502 and body["code"] == "bmc_unreachable"


def test_delete_credential(admin, host):
    """正向：删除凭据 204，重复删除 404。"""
    admin("PUT", f"/api/v1/hosts/{host}/bmc", body={
        "bmc_ip": "10.80.0.9", "username": "admin", "password": "x"}, expect=204)
    status, _ = admin("DELETE", f"/api/v1/hosts/{host}/bmc")
    assert status == 204
    assert db.bmc_credential(host) is None
    status, _body = admin("DELETE", f"/api/v1/hosts/{host}/bmc")
    assert status == 404


def test_collect_runs_endpoint(admin):
    """采集执行记录接口：结构完整（W4 的 collect_run 查询口）。"""
    _, body = admin("GET", "/api/v1/collect-runs?limit=10", expect=200)
    for key in ("items", "total"):
        assert key in body


def test_put_bmc_missing_host_404(admin):
    """边界：为不存在的主机录入凭据应 404。"""
    status, _body = admin("PUT", "/api/v1/hosts/999999/bmc", body={
        "bmc_ip": "10.80.0.9", "username": "u", "password": "p"})
    assert status == 404


def test_put_bmc_ipv6_and_redfish(admin, host):
    """边界：IPv6 BMC 地址与 redfish 协议均为合法输入。"""
    _status, _ = admin("PUT", f"/api/v1/hosts/{host}/bmc", body={
        "bmc_ip": "fe80::1", "username": "u", "password": "p", "protocol": "redfish"},
        expect=204)
    cred = db.bmc_credential(host)
    assert cred is not None and cred["protocol"] == "redfish"


# ---------------------------------------------------------------------------
# W15：BMC 管控（capability / command / audit）
# ---------------------------------------------------------------------------


def test_command_input_validation(admin, host):
    """指令校验：非法 cmd_type / 非法 power_action / 转速越界均 422。"""
    for bad in (
        {"cmd_type": "reboot-everything"},
        {"cmd_type": "power", "power_action": "boom"},
        {"cmd_type": "fan", "speed_percent": 0, "auto_mode": False},
        {"cmd_type": "fan", "speed_percent": 101},
        {"cmd_type": "fan"},
        {"cmd_type": "identify", "duration_sec": 4000},
        {"cmd_type": "policy", "target": "magic"},
    ):
        status, body = admin("POST", f"/api/v1/bmc/{host}/command", body=bad)
        assert status == 422 and body["code"] == "invalid_input", \
            f"{bad} 应 422, got {status}"


def test_command_requires_bmc(admin, host):
    """未配置 BMC 的主机：指令与能力探测均 404。"""
    status, _ = admin("POST", f"/api/v1/bmc/{host}/command",
                      body={"cmd_type": "power", "power_action": "on"})
    assert status == 404
    status, _ = admin("GET", f"/api/v1/bmc/{host}/capability")
    assert status == 404


def test_command_missing_host_404(admin):
    """边界：对不存在的主机下发指令应 404。"""
    status, _ = admin("POST", "/api/v1/bmc/999999/command",
                      body={"cmd_type": "power", "power_action": "on"})
    assert status == 404


@pytest.mark.skipif(shutil.which("ipmitool") is not None,
                    reason="本机有 ipmitool 时该用例会真连 BMC（超时慢），交给真机验证")
def test_command_execution_failure_recorded(admin, host):
    """无 ipmitool 环境下发指令：HTTP 200 + ok=false（业务失败≠API 失败），
    审计表留下 failed 记录——执行记录即操作审计（W15 核心语义）。"""
    admin("PUT", f"/api/v1/hosts/{host}/bmc", body={
        "bmc_ip": "10.80.0.9", "username": "admin", "password": "x"}, expect=204)
    status, body = admin("POST", f"/api/v1/bmc/{host}/command",
                         body={"cmd_type": "fan", "speed_percent": 60})
    assert status == 200 and body["ok"] is False
    assert body["request_id"].startswith("bmc-")

    _status, audit = admin("GET", "/api/v1/bmc/audit?limit=50", expect=200)
    mine = [e for e in audit["items"] if e["host_id"] == host]
    assert mine, "审计里应有本次失败指令"
    entry = mine[0]
    assert entry["result"] == "failed" and entry["cmd_type"] == "fan"
    assert entry["operator"] and entry["created_at"]
    for key in ("id", "host_id", "operator", "cmd_type", "result", "created_at"):
        assert key in entry
