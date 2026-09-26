"""测试环境配置：所有路径与地址集中此处，支持环境变量覆盖。

环境变量：
    MW_BASE_URL   服务端地址（默认 http://127.0.0.1:18080）
    MW_DATA_DIR   服务端数据目录（默认 <repo>/backend/tmp-data）
    MW_HOOK_PORT  Webhook 接收器端口（默认 18101）
    MW_FRONTEND   前端 dev server 地址（默认 http://localhost:5173）
"""
import os
import pathlib

# 仓库根目录：tests/python/mw/config.py → 上溯三级
REPO_ROOT = pathlib.Path(__file__).resolve().parents[3]

BASE_URL = os.environ.get("MW_BASE_URL", "http://127.0.0.1:18080")
DATA_DIR = pathlib.Path(os.environ.get("MW_DATA_DIR", REPO_ROOT / "backend" / "tmp-data"))
DB_PATH = DATA_DIR / "metalwatch.db"
BOOTSTRAP_FILE = DATA_DIR / "bootstrap_admin.txt"

# 本机 Agent（离线判定集成测试需要；缺失时相关用例自动跳过）
AGENT_EXE = REPO_ROOT / "agent" / "bin-local" / "agent.exe"
AGENT_TOKEN_FILE = REPO_ROOT / "agent" / "bin-local" / "token"
AGENT_HOST_ID = int(os.environ.get("MW_AGENT_HOST_ID", "1"))
AGENT_SPOOL = REPO_ROOT / "agent" / "bin-local" / "spool"

FRONTEND_URL = os.environ.get("MW_FRONTEND", "http://localhost:5173")
HOOK_PORT = int(os.environ.get("MW_HOOK_PORT", "18101"))

# 请求与轮询默认参数
HTTP_TIMEOUT = 15
POLL_TIMEOUT = 30
POLL_INTERVAL = 0.3
