# -*- coding: utf-8 -*-
"""Allure 支撑层：优雅降级封装。

allure-pytest 已安装 → 提供装饰器/步骤/附件的真实实现；
未安装 → 全部退化为 no-op，保证套件在任何环境可跑（与 D34 兼容）。
"""
import contextlib
import json

try:
    import allure
    ALLURE_AVAILABLE = True
except ImportError:  # pragma: no cover - 环境无 allure 时降级
    ALLURE_AVAILABLE = False


def _noop(*args, **kwargs):
    def deco(fn):
        return fn
    return deco


if ALLURE_AVAILABLE:
    feature = allure.feature
    story = allure.story
    severity = allure.severity
    title = allure.title
    step = allure.step
    dynamic = allure.dynamic
    ATTACH_JSON = allure.attachment_type.JSON
    ATTACH_TEXT = allure.attachment_type.TEXT
else:
    feature = story = severity = title = _noop
    dynamic = None

    @contextlib.contextmanager
    def step(_msg):
        yield
    ATTACH_JSON = ATTACH_TEXT = "text/plain"


def attach(name, body, attachment_type=None):
    """向当前用例附加内容（allure 不可用时 no-op）。"""
    if not ALLURE_AVAILABLE:
        return
    allure.attach(name, body, attachment_type)


def attach_json(name, data):
    """附加 JSON 数据（dict/任意可序列化对象）。"""
    if not ALLURE_AVAILABLE:
        return
    allure.attach(name, json.dumps(data, ensure_ascii=False, indent=1, default=str),
                  ATTACH_JSON)


def attach_http(method, path, status, request_body, response_body):
    """附加一次 HTTP 交互（client.call 自动调用，失败诊断的核心证据）。"""
    if not ALLURE_AVAILABLE:
        return
    req = json.dumps(request_body, ensure_ascii=False, default=str) if request_body else "-"
    resp = json.dumps(response_body, ensure_ascii=False, default=str) if not isinstance(
        response_body, str) else response_body
    allure.attach(f"{method} {path} → {status} · request", req, ATTACH_JSON)
    allure.attach(f"{method} {path} → {status} · response", resp, ATTACH_JSON)


def auto_meta(request, base_url=None, server_version=None):
    """按用例所在文件自动标注 feature / story / title / severity。

    在 autouse 夹具内调用（allure.dynamic 要求运行期调用）：
      feature  = 套件目录（冒烟/单元/集成）
      story    = 模块名
      title    = docstring 首行（缺省用节点名）
      severity = smoke/integration → CRITICAL，unit → NORMAL
    """
    if not ALLURE_AVAILABLE:
        return
    import os
    suite = os.path.basename(os.path.dirname(request.node.fspath))   # smoke/unit/integration
    module = os.path.splitext(os.path.basename(request.node.fspath))[0]  # test_xxx
    feature_map = {"smoke": "冒烟 · 核心路径", "unit": "单元 · 接口契约", "integration": "集成 · 跨模块协作"}
    severity_map = {"smoke": allure.severity_level.CRITICAL,
                    "unit": allure.severity_level.NORMAL,
                    "integration": allure.severity_level.CRITICAL}
    allure.dynamic.feature(feature_map.get(suite, suite))
    allure.dynamic.story(module)
    # title 不在此处设置：夹具内的 dynamic.title 会被插件在调用阶段覆盖，
    # 由 run_tests.py 在运行后统一用 docstring（description）回写 name。
    allure.dynamic.severity(severity_map.get(suite, allure.severity_level.NORMAL))
    if base_url:
        allure.dynamic.description_html(
            f"<b>被测服务</b>：{base_url}" + (f" · <b>版本</b> {server_version}" if server_version else ""))
