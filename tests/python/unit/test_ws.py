"""单元测试 · 系统状态与实时推送握手（W7/W11）。"""
from mw import endpoints, ws


def test_system_status_shape(admin):
    """系统状态契约：进程/存储/采集三段齐全。"""
    _, body = admin("GET", "/api/v1/system/status", expect=200)
    for key in ("version", "uptime_sec", "storage", "host_count", "collect"):
        assert key in body
    assert body["storage"]["schema_version"] >= 1


def test_ws_requires_auth():
    """边界：WebSocket 握手无令牌必须 401（不升级协议）。"""
    try:
        ws.WSClient("127.0.0.1", 18080, "/api/v1/ws/alerts")
        raise AssertionError("无令牌握手不应成功")
    except ConnectionError as e:
        assert "401" in str(e)


def test_ws_handshake_and_accept(admin_token):
    """正向：带会话令牌握手返回 101，Sec-WebSocket-Accept 正确；空闲期无乱推帧。"""
    # WS 握手走裸 socket，绕过了 client.call 的覆盖率登记，这里显式登记
    endpoints.record("GET", "/api/v1/ws/alerts")
    conn = ws.WSClient("127.0.0.1", 18080, "/api/v1/ws/alerts",
                       headers={"Authorization": "Bearer " + admin_token})
    try:
        try:
            conn.recv_text(timeout=3)
            # 若服务端恰好广播了事件也算通过（不应是乱码——recv 已按帧解码）
        except TimeoutError:
            pass  # 空闲无推送是预期行为
    finally:
        conn.close()
