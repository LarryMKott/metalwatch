"""pytest 共享夹具：环境配置、管理员会话、覆盖率登记。

前置条件（缺失时相关用例自动跳过/报错提示）：
  1. 本地服务端已启动（MW_BASE_URL，默认 http://127.0.0.1:18080）
  2. 引导管理员文件存在（backend/tmp-data/bootstrap_admin.txt）
"""
import json

import pytest
from mw import caseregistry, client, endpoints
from mw import config as mw_config
from mw.allure_support import ALLURE_AVAILABLE, auto_meta

# Allure 运行期采集的信息，sessionfinish 写入结果目录
_allure_env = {"base_url": mw_config.BASE_URL}


@pytest.fixture(scope="session")
def env():
    """全局配置命名空间（mw.config）。"""
    return mw_config


@pytest.fixture(autouse=True)
def _allure_auto_meta(request):
    """自动为每个用例标注 Allure 元数据 + 记录用例 ID + 捕获 HTTP 步骤。"""
    import os
    module = os.path.splitext(os.path.basename(request.node.fspath))[0]
    name = request.node.name
    meta = caseregistry.get(module, name)

    if ALLURE_AVAILABLE:
        import allure
        allure.dynamic.label("testId", meta["id"])
    client.begin_capture()
    auto_meta(request, base_url=mw_config.BASE_URL,
              server_version=_allure_env.get("server_version"))
    yield
    steps = client.end_capture()
    _dump_steps(request.node, module, name, meta, steps)


def _dump_steps(node, module, name, meta, steps):
    """把用例的实际执行步骤写入报告目录（详细报告的数据源）。"""
    import hashlib
    doc = (node.function.__doc__ or "").strip().splitlines()
    payload = {
        "nodeid": node.nodeid,
        "module": module,
        "name": name,
        "case_id": meta["id"],
        "objective": doc[0] if doc else "",
        "preconditions": meta.get("pre", caseregistry.P_SERVER),
        "expected": meta.get("expected", "见实际执行结果"),
        "steps": steps or [],
    }
    out = mw_config.REPO_ROOT / "tests" / "python" / "reports" / "steps"
    out.mkdir(parents=True, exist_ok=True)
    key = hashlib.md5(node.nodeid.encode()).hexdigest()[:12]
    (out / f"{key}.json").write_text(
        json.dumps(payload, ensure_ascii=False, indent=1), encoding="utf-8")


@pytest.fixture(scope="session")
def admin_password():
    """引导管理员初始口令。"""
    return client.require_admin_password()


@pytest.fixture(scope="session")
def admin_token(admin_password):
    """登录一次、整个会话复用的管理员令牌。"""
    return client.login(password=admin_password)


@pytest.fixture(scope="session")
def admin(admin_token):
    """带鉴权的请求便捷函数：admin("GET", "/api/v1/hosts")。"""
    class _Admin:
        token = admin_token

        def __call__(self, method, path, **kw):
            kw.setdefault("token", admin_token)
            return client.call(method, path, **kw)

    return _Admin()


@pytest.fixture(scope="session")
def agent_info():
    """本机 Agent 的 (token, host_id)；未部署时返回 None。"""
    try:
        token = mw_config.AGENT_TOKEN_FILE.read_text(encoding="utf-8").strip()
        return token, mw_config.AGENT_HOST_ID
    except OSError:
        return None


def pytest_sessionfinish(session, exitstatus):
    """会话结束：写端点命中（覆盖率汇总）+ Allure environment.properties。"""
    out = mw_config.REPO_ROOT / "tests" / "python" / "reports" / "coverage_hits.json"
    try:
        endpoints.dump(out)
    except OSError:
        pass

    try:
        alluredir = session.config.getoption("--alluredir")
    except (ValueError, AttributeError):
        alluredir = None
    if alluredir:
        import pathlib
        import platform
        props = pathlib.Path(alluredir) / "environment.properties"
        info = {
            "base.url": mw_config.BASE_URL,
            "python.version": platform.python_version(),
            "platform": platform.platform(),
            "data.dir": str(mw_config.DATA_DIR),
        }
        if _allure_env.get("server_version"):
            info["server.version"] = _allure_env["server_version"]
        nl = chr(10)
        props.write_text(nl.join(f"{k}={v}" for k, v in info.items()) + nl,
                         encoding="utf-8")


def pytest_configure(config):
    """会话启动时探一次服务端版本，供 Allure 环境信息展示。"""
    try:
        import urllib.request
        with urllib.request.urlopen(mw_config.BASE_URL + "/healthz", timeout=3) as resp:
            import json as _json
            _allure_env["server_version"] = _json.load(resp).get("version", "")
    except OSError:
        pass
