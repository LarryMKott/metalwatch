"""单元测试 · 告警中心接口（W7）：模板、查询参数验证、确认幂等。"""



def test_templates_shape(admin):
    """内置阈值模板契约：字段齐全、级别合法。"""
    _, tpls = admin("GET", "/api/v1/alerts/templates", expect=200)
    assert len(tpls) >= 6
    for t in tpls:
        for key in ("id", "name", "metric", "op", "threshold", "severity", "for_duration", "enabled"):
            assert key in t, f"模板缺字段 {key}"
        assert t["severity"] in ("critical", "major", "minor", "info")
        assert t["op"] in (">", ">=", "<", "<=", "==")


def test_alerts_state_validation(admin):
    """输入验证：非法 state 参数应 422。"""
    status, body = admin("GET", "/api/v1/alerts?state=bogus")
    assert status == 422 and body["code"] == "invalid_input"


def test_alerts_list_all_shape(admin):
    """正向：state=all 返回列表（可能为空），元素字段符合前端 Alert 契约。"""
    _, alerts = admin("GET", "/api/v1/alerts?state=all", expect=200)
    assert isinstance(alerts, list)
    for a in alerts:
        for key in ("id", "host_id", "severity", "rule", "state", "fired_at"):
            assert key in a
        assert a["severity"] in ("critical", "major", "minor", "info")
        assert a["state"] in ("active", "acked", "resolved", "suppressed")


def test_ack_missing_alert_404(admin):
    """边界：确认不存在的告警返回 404。"""
    status, _body = admin("POST", "/api/v1/alerts/99999999/ack")
    assert status == 404


def test_state_filters_disjoint(admin):
    """语义：active 与 resolved 查询结果不应重叠。"""
    _, active = admin("GET", "/api/v1/alerts?state=active", expect=200)
    _, resolved = admin("GET", "/api/v1/alerts?state=resolved", expect=200)
    active_ids = {a["id"] for a in active}
    resolved_ids = {a["id"] for a in resolved}
    assert not (active_ids & resolved_ids)


def test_alerts_severity_filter(admin):
    """过滤条件：severity=bogus 为未知级别 → 空列表而非报错（行为记录）。"""
    _status, body = admin("GET", "/api/v1/alerts?state=all&severity=bogus", expect=200)
    assert isinstance(body, list) and body == []


def test_alerts_host_id_non_numeric_ignored(admin):
    """边界：host_id 非数字时被忽略（不过滤），返回正常列表结构。"""
    _status, body = admin("GET", "/api/v1/alerts?state=all&host_id=abc", expect=200)
    assert isinstance(body, list)
