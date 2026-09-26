"""单元测试 · 内嵌 WebUI 静态托管（D42）：入口页、SPA 回落、API 契约不被遮蔽。

前端是 hash 路由，服务端只须伺服 index.html 与静态资源；
关键契约是「/api 前缀的 404 不被 SPA 回落吞掉」。
"""
from mw import client


def test_index_served_anonymously():
    """GET / 返回 HTML 入口页，且不鉴权——登录页必须匿名可达（数据由 /api 鉴权）。"""
    _status, body = client.call("GET", "/", expect=200)
    assert isinstance(body, str), f"期望 HTML 文本，实际: {type(body)}"
    assert body.lstrip().lower().startswith("<!doctype html"), body[:80]


def test_index_also_at_index_html():
    """GET /index.html 与 / 同源同文。"""
    _status, body = client.call("GET", "/index.html", expect=200)
    assert isinstance(body, str) and "html" in body[:100].lower()


def test_unknown_path_falls_back_to_index():
    """非 /api 未知路径回落 index.html（SPA 兜底），而不是 404。"""
    _status, body = client.call("GET", "/hosts/1", expect=200)
    assert isinstance(body, str) and body.lstrip().lower().startswith("<!doctype html")


def test_api_404_contract_unchanged(admin_password=None):
    """/api 未知路径仍是 JSON 404（code=not_found），不被 SPA 回落吞掉。"""
    status, body = client.call("GET", "/api/v1/nonexistent")
    assert status == 404
    assert isinstance(body, dict) and body["code"] == "not_found"


def test_api_requires_auth_even_as_fallback_path():
    """未认证的 /api 请求仍走 401 拦截（WebUI 挂载不得影响鉴权链）。"""
    status, body = client.call("GET", "/api/v1/hosts")
    assert status == 401 and body["code"] == "unauthorized"
