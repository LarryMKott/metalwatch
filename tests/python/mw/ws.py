"""最小 WebSocket 客户端（仅客户端侧读帧），用标准库完成握手。

服务端（gorilla/websocket）→ 客户端方向的帧不带掩码；
本客户端只读不写，用于验证 /api/v1/ws/alerts 的实时推送。
"""
import base64
import hashlib
import http.client
import json
import os
import struct

_MAGIC = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"


class WSClient:
    def __init__(self, host, port, path, headers=None, timeout=10):
        """完成 RFC6455 握手。status != 101 时抛 ConnectionError（附状态码）。"""
        self._conn = http.client.HTTPConnection(host, port, timeout=timeout)
        key = base64.b64encode(os.urandom(16)).decode()
        self._conn.putrequest("GET", path)
        self._conn.putheader("Upgrade", "websocket")
        self._conn.putheader("Connection", "Upgrade")
        self._conn.putheader("Sec-WebSocket-Key", key)
        self._conn.putheader("Sec-WebSocket-Version", "13")
        for k, v in (headers or {}).items():
            self._conn.putheader(k, v)
        self._conn.endheaders()
        resp = self._conn.getresponse()
        self.status = resp.status
        if resp.status != 101:
            body = resp.read()[:200]
            raise ConnectionError(f"握手失败 HTTP {resp.status}: {body!r}")
        expected = base64.b64encode(
            hashlib.sha1((key + _MAGIC).encode()).digest()).decode()
        if resp.getheader("Sec-WebSocket-Accept") != expected:
            raise ConnectionError("Sec-WebSocket-Accept 校验失败")
        self._sock = self._conn.sock
        self._sock.settimeout(timeout)

    def recv_text(self, timeout=10):
        """读取一帧文本（自动跳过 ping/pong 等控制帧），超时抛 TimeoutError。"""
        self._sock.settimeout(timeout)
        while True:
            hdr = self._recv_exact(2)
            opcode = hdr[0] & 0x0F
            length = hdr[1] & 0x7F
            if length == 126:
                length = struct.unpack(">H", self._recv_exact(2))[0]
            elif length == 127:
                length = struct.unpack(">Q", self._recv_exact(8))[0]
            payload = self._recv_exact(length) if length else b""
            if opcode == 0x1:  # text
                return payload.decode("utf-8")
            if opcode == 0x8:  # close
                raise ConnectionError("服务端关闭连接")
            # ping(0x9)/pong(0xA)/其他控制帧：跳过继续读

    def recv_json(self, timeout=10):
        return json.loads(self.recv_text(timeout))

    def close(self):
        try:
            self._sock.close()
        except OSError:
            pass
        self._conn.close()

    def _recv_exact(self, n):
        buf = b""
        while len(buf) < n:
            chunk = self._sock.recv(n - len(buf))
            if not chunk:
                raise ConnectionError("连接提前关闭")
            buf += chunk
        return buf
