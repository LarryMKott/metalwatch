# -*- coding: utf-8 -*-
"""集成测试 · Agent 离线判定（W7/M3 验收 3）：

停止本机 Agent → 超 2×采集周期未上报 → 置 offline + agent_offline 告警；
重启 Agent → 恢复上报 → 自动 resolved。

依赖本机 Agent 部署（agent/bin-local/agent.exe + token 文件），缺失则跳过。
标注 slow：真实等待离线窗口，约 3 分钟。
"""
import pytest

from mw import agentproc, client, config, db
from mw.allure_support import step


def _host_status(admin, host_id):
    _, body = admin("GET", f"/api/v1/hosts/{host_id}", expect=200)
    return body["status"]


@pytest.mark.slow
def test_offline_detection_and_recovery(admin, agent_info):
    if agent_info is None:
        pytest.skip("本机 Agent 未部署（缺少令牌文件）")
    if not config.AGENT_EXE.exists():
        pytest.skip("Agent 二进制不存在")
    host_id = config.AGENT_HOST_ID
    if _host_status(admin, host_id) != "online":
        pytest.skip("目标主机不在线，跳过（可能上轮用例仍在恢复）")

    # 1) 停 Agent：staleAfter = 2×采集周期（默认 60s），判定循环 15s 一轮
    with step("停止本机 Agent（staleAfter = 2×采集周期）"):
        was_running = agentproc.is_running()
        if was_running:
            agentproc.stop()
    try:
        def offline_raised():
            status = _host_status(admin, host_id)
            rows = db.alerts_for_host(host_id, category="agent_offline", state="firing")
            return (status == "offline" and rows) or None
        client.wait_until(offline_raised, timeout=120, interval=5,
                          what="主机置离线且 agent_offline 触发")
        row = db.alerts_for_host(host_id, category="agent_offline", state="firing")[0]
        assert row["severity"] == "info"

        # 幂等：窗口内重复判定不产生第二条
        with step("幂等断言：窗口内重复判定不产生新告警"):
            n = len(db.alerts_for_host(host_id, category="agent_offline", state="firing"))
            import time
            time.sleep(20)
            assert len(db.alerts_for_host(host_id, category="agent_offline", state="firing")) == n
    finally:
        # 2) 重启 Agent：恢复上报 → 自动 resolved
        with step("重启 Agent：恢复上报 → 自动 resolved"):
            agentproc.start()
        def recovered():
            status = _host_status(admin, host_id)
            firing = db.alerts_for_host(host_id, category="agent_offline", state="firing")
            return (status == "online" and not firing) or None
        client.wait_until(recovered, timeout=180, interval=5,
                          what="主机恢复在线且 agent_offline 解除")

    # 故障史保留
    history = db.alerts_for_host(host_id, category="agent_offline", state="resolved")
    assert history, "resolved 后故障史应保留"
