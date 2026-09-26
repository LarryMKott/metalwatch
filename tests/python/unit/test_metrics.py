"""单元测试 · 时序查询接口（W1c）：参数验证与曲线契约。

只读测试：消费本机 Agent 已写入的 up 指标；无 Agent 时跳过数据断言。
"""
import time

import pytest


def _iso(offset_sec=0):
    t = time.gmtime(time.time() + offset_sec)
    return time.strftime("%Y-%m-%dT%H:%M:%SZ", t)


def test_metric_required_422(admin):
    """输入验证：缺 metric 参数应 422。"""
    status, body = admin("GET", "/api/v1/hosts/1/metrics")
    assert status == 422 and body["code"] == "invalid_input"


def test_bad_time_format_422(admin):
    """输入验证：from 非 RFC3339 应 422。"""
    status, _ = admin("GET", "/api/v1/hosts/1/metrics?metric=up&from=not-a-time")
    assert status == 422


def test_from_not_before_to_422(admin):
    """边界：from 晚于 to 应 422。"""
    status, _ = admin("GET",
                      f"/api/v1/hosts/1/metrics?metric=up&from={_iso(0)}&to={_iso(-3600)}")
    assert status == 422


def test_bad_step_422(admin):
    """输入验证：step 非时长格式应 422。"""
    status, _ = admin("GET", "/api/v1/hosts/1/metrics?metric=up&step=abc")
    assert status == 422


def test_too_small_step_422(admin):
    """边界：step 小于 1s 应 422。"""
    status, _ = admin("GET", "/api/v1/hosts/1/metrics?metric=up&step=10ms")
    assert status == 422


def test_up_metric_curve(admin, agent_info):
    """正向：up 指标曲线有数据、结构符合前端 MetricSeries 契约。

    门禁以实际状态为准：目标主机不存在或离线（全新环境/CI）→ 跳过。
    """
    status, host = admin("GET", "/api/v1/hosts/1")
    if status != 200 or host.get("status") != "online":
        pytest.skip(f"主机 1 不可用（status={host.get('status') if host else status}），无实时数据可查")
    status, body = admin("GET",
                         f"/api/v1/hosts/1/metrics?metric=up&from={_iso(-7200)}&to={_iso(60)}",
                         expect=200)
    assert body["metric"] == "up"
    assert body["source"] == "embedded"
    assert "downsampled" in body
    assert body["points"], "Agent 在线时 up 指标应有数据点"
    _ts, value = body["points"][-1]
    assert value == 1.0


def test_labels_filter_no_match(admin):
    """标签过滤：不存在的标签组合返回空 points 而非报错。"""
    _status, body = admin("GET",
                         "/api/v1/hosts/1/metrics?metric=up&labels=device:nope",
                         expect=200)
    assert body["points"] == []


def test_metric_nonexistent_host_empty(admin):
    """行为记录：时序接口不校验主机存在性——不存在的 host_id 返回 200 空点（W1c 设计取舍）。"""
    _status, body = admin("GET", "/api/v1/hosts/999999/metrics?metric=up", expect=200)
    assert body["points"] == []


def test_metric_future_range_empty(admin):
    """边界：查询区间整体位于未来 → 无数据点。"""
    _status, body = admin("GET",
                         f"/api/v1/hosts/1/metrics?metric=up&from={_iso(7200)}&to={_iso(14400)}",
                         expect=200)
    assert body["points"] == []


def test_metric_long_range_downsampled(admin, agent_info):
    """聚合档切换：>24h 区间响应 downsampled=true（数据回退 raw 降采样）。"""
    if agent_info is None:
        pytest.skip("本机 Agent 未部署")
    _status, body = admin("GET",
                         f"/api/v1/hosts/1/metrics?metric=up&from={_iso(-30*3600)}&to={_iso(60)}",
                         expect=200)
    assert body["downsampled"] is True
