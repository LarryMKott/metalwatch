"""REST 客户端：基于 urllib 的薄封装，自动登记端点覆盖率命中。"""
import json
import time
import urllib.error
import urllib.request

from . import config, endpoints
from .allure_support import attach_http


class ApiError(Exception):
    """非 2xx 响应。保留服务端统一错误体的 code / message。"""

    def __init__(self, status, body):
        self.status = status
        self.body = body
        if isinstance(body, dict):
            self.code = body.get("code", "")
            self.message = body.get("message", "")
        else:
            self.code, self.message = "", str(body)
        super().__init__(f"HTTP {status}: {self.code} {self.message}")


# 当前用例的步骤捕获缓冲（conftest 夹具管理生命周期）
_current_steps = None


def begin_capture():
    """开始捕获当前用例的全部 HTTP 交互（autouse 夹具在 setup 时调用）。"""
    global _current_steps
    _current_steps = []


def end_capture():
    """结束捕获并返回交互列表（无捕获时返回 None）。"""
    global _current_steps
    out = _current_steps
    _current_steps = None
    return out


def call(method, path, token=None, body=None, expect=None, timeout=None):
    """发起一次 REST 请求。

    返回 (status, parsed_body)。expect 给定时断言状态码在此集合内。
    body 为 dict 时以 JSON 编码发送。每次调用自动登记端点命中。
    """
    url = config.BASE_URL + path
    data = json.dumps(body).encode("utf-8") if body is not None else None
    req = urllib.request.Request(url, data=data, method=method.upper())
    req.add_header("Content-Type", "application/json")
    if token:
        req.add_header("Authorization", "Bearer " + token)
    try:
        with urllib.request.urlopen(req, timeout=timeout or config.HTTP_TIMEOUT) as resp:
            status = resp.status
            raw = resp.read()
    except urllib.error.HTTPError as e:
        status = e.code
        raw = e.read()

    try:
        parsed = json.loads(raw) if raw else None
    except ValueError:
        parsed = raw.decode("utf-8", "replace")

    # 覆盖率登记剥离查询串，与端点模板对齐
    endpoints.record(method, path.split("?", 1)[0])
    if _current_steps is not None:
        _current_steps.append({
            "method": method, "path": path, "status": status,
            "request": body, "response": parsed,
        })
    attach_http(method, path, status,
                body if body is not None else "-",
                parsed if parsed is not None else "-")
    if expect is not None and status not in (expect if isinstance(expect, (set, tuple, list)) else {expect}):
        raise ApiError(status, parsed)
    return status, parsed


def login(username="admin", password=None):
    """登录并返回会话令牌。密码缺省从引导管理员文件读取。"""
    password = password or require_admin_password()
    _, resp = call("POST", "/api/v1/auth/login",
                   body={"username": username, "password": password}, expect=200)
    return resp["token"]


def wait_until(cond, timeout=None, interval=None, what="condition"):
    """轮询等待条件成立（异步管道 flushed 的场景），超时抛 AssertionError。"""
    timeout = timeout or config.POLL_TIMEOUT
    interval = interval or config.POLL_INTERVAL
    deadline = time.time() + timeout
    last = None
    while time.time() < deadline:
        last = cond()
        if last:
            return last
        time.sleep(interval)
    raise AssertionError(f"等待超时（{timeout}s）: {what}，最后结果={last!r}")


def admin_password():
    """从引导管理员文件解析初始口令。"""
    text = config.BOOTSTRAP_FILE.read_text(encoding="utf-8")
    for line in text.splitlines():
        if line.startswith("password:"):
            return line.split(":", 1)[1].strip()
    raise RuntimeError(f"引导口令文件格式不符: {config.BOOTSTRAP_FILE}")


def require_admin_password():
    try:
        return admin_password()
    except (OSError, RuntimeError):
        raise RuntimeError(
            f"未找到引导口令文件 {config.BOOTSTRAP_FILE}——"
            "请确认本地服务端已启动（backend/tmp-data 目录存在）")
