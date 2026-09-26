# -*- coding: utf-8 -*-
"""本机 Agent 进程管理：供离线判定集成测试启停 Agent。

仅 Windows 本机场景设计；进程名固定为 agent.exe（bin-local 产物名）。
"""
import os
import subprocess
import time

from . import config

_PROC_NAME = "agent.exe"


def is_running():
    out = subprocess.run(["tasklist", "/FI", f"IMAGENAME eq {_PROC_NAME}"],
                         capture_output=True, text=True).stdout
    return _PROC_NAME.lower() in out.lower()


def stop():
    subprocess.run(["taskkill", "/F", "/IM", _PROC_NAME],
                   capture_output=True, text=True)
    deadline = time.time() + 5
    while time.time() < deadline and is_running():
        time.sleep(0.2)


def start(server_url=None, interval="15s"):
    """以 gRPC 模式拉起本机 Agent（令牌与 host_id 来自约定文件/配置）。"""
    token = config.AGENT_TOKEN_FILE.read_text(encoding="utf-8").strip()
    env = dict(os.environ)
    env["METALWATCH_AGENT_TOKEN"] = token
    env["METALWATCH_HOST_ID"] = str(config.AGENT_HOST_ID)
    config.AGENT_SPOOL.mkdir(parents=True, exist_ok=True)
    log = open(config.AGENT_SPOOL / "agent-pytest.log", "ab")
    proc = subprocess.Popen(
        [str(config.AGENT_EXE), "--server", server_url or config.BASE_URL,
         "--spool", str(config.AGENT_SPOOL), "--interval", interval],
        env=env, stdout=log, stderr=subprocess.STDOUT,
        creationflags=subprocess.CREATE_NO_WINDOW)
    time.sleep(1)
    if proc.poll() is not None:
        raise RuntimeError("Agent 进程启动即退出，请检查日志 " + str(config.AGENT_SPOOL))
    return proc
