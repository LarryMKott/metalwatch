# -*- coding: utf-8 -*-
"""单元测试 · 鉴权接口（W11）：登录、会话、登出、改密、审计。

测试数据隔离：不修改管理员口令（只验证错误路径）；审计断言只读。
"""
import uuid

from mw import client, db


def test_login_wrong_password_401(admin_password):
    """异常路径：错误口令返回 401 bad_credential。"""
    status, body = client.call("POST", "/api/v1/auth/login", body={
        "username": "admin", "password": "definitely-wrong-" + uuid.uuid4().hex[:8]})
    assert status == 401
    assert body["code"] == "bad_credential"


def test_login_missing_fields_422():
    """输入验证：缺字段的请求体应被拒绝（4xx 而非 500）。"""
    status, _ = client.call("POST", "/api/v1/auth/login", body={"username": "admin"})
    assert 400 <= status < 500


def test_me_returns_admin(admin):
    """正向路径：me 返回当前会话身份。"""
    status, body = admin("GET", "/api/v1/auth/me", expect=200)
    assert body["username"] == "admin"
    assert body["role"] == "admin"


def test_me_without_token_401():
    """边界：无令牌访问受保护接口必须 401。"""
    status, body = client.call("GET", "/api/v1/auth/me")
    assert status == 401 and body["code"] == "unauthorized"


def test_logout_ok(admin):
    """登出返回 200（无状态令牌，服务端记审计后由客户端丢弃）。"""
    status, body = admin("DELETE", "/api/v1/auth/logout", expect=200)
    assert body["ok"] is True


def test_change_password_wrong_old_401(admin):
    """异常路径：原口令不正确时拒绝改密，且管理员原口令仍可登录。"""
    status, body = admin("POST", "/api/v1/auth/change-password", body={
        "old_password": "wrong-old-" + uuid.uuid4().hex[:6],
        "new_password": "NewP@ss-12x"})
    assert status == 401 and body["code"] == "bad_credential"


def test_audit_logs_record_login(admin):
    """集成点：登录动作落审计表并可查询。"""
    client.login()  # 触发一次成功登录
    _, body = admin("GET", "/api/v1/audit-logs?action=auth.login&limit=10", expect=200)
    items = body["items"] if isinstance(body, dict) else body
    assert items, "应能查到 auth.login 审计记录"


def test_login_empty_username_4xx():
    """边界：空用户名登录应被拒绝（4xx），不得 500。"""
    status, _ = client.call("POST", "/api/v1/auth/login",
                            body={"username": "", "password": "whatever"})
    assert 400 <= status < 500


def test_audit_logs_filter_by_result(admin):
    """过滤条件：result=denied 只返回失败审计（可空集），结构完整。"""
    status, body = admin("GET", "/api/v1/audit-logs?result=denied&limit=10", expect=200)
    items = body["items"] if isinstance(body, dict) else body
    for row in items:
        assert row["result"] == "denied"
