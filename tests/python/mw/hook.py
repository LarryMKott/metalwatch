"""Webhook 接收器：本地 HTTP 服务捕获出站投递，供集成测试断言。"""
import json
import subprocess
import sys
import time
import urllib.request
from pathlib import Path

from . import config

_SERVER = Path(__file__).parent / "hook_server.py"


class WebhookReceiver:
    """管理 127.0.0.1:<port> 上的捕获服务；捕获内容写入 JSONL 文件。"""

    def __init__(self, port=None):
        self.port = port or config.HOOK_PORT
        self.capture_file = Path(config.REPO_ROOT) / "tests" / "python" / "reports" / "hook_capture.jsonl"
        self._proc = None

    def start(self):
        self.capture_file.parent.mkdir(parents=True, exist_ok=True)
        self.clear()
        self._proc = subprocess.Popen(
            [sys.executable, str(_SERVER), str(self.port), str(self.capture_file)],
            stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        deadline = time.time() + 5
        while time.time() < deadline:
            try:
                urllib.request.urlopen(f"http://127.0.0.1:{self.port}/__ping", timeout=1)
                return self
            except urllib.error.HTTPError:
                return self  # 404 也说明服务已监听
            except OSError:
                time.sleep(0.1)
        raise RuntimeError("webhook 接收器启动失败")

    def clear(self):
        self.capture_file.write_text("", encoding="utf-8")

    def capture(self):
        """返回已捕获的投递记录列表。"""
        if not self.capture_file.exists():
            return []
        out = []
        for line in self.capture_file.read_text(encoding="utf-8").splitlines():
            if line.strip():
                out.append(json.loads(line))
        return out

    def stop(self):
        if self._proc:
            self._proc.terminate()
            try:
                self._proc.wait(timeout=3)
            except subprocess.TimeoutExpired:
                self._proc.kill()
            self._proc = None
