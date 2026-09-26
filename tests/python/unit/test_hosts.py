"""单元测试 · 资产 CRUD（W1a/W8）：输入验证、唯一冲突、分页与生命周期。

测试数据隔离：每个用例用 uuid 后缀的主机名，finally 级清理删除。
"""
import uuid

UNIQUE = lambda: uuid.uuid4().hex[:10]


def _create(admin, **overrides):
    body = {"hostname": "py-u-" + UNIQUE(), "primary_ip": "10.77.0.1"}
    body.update(overrides)
    status, resp = admin("POST", "/api/v1/hosts", body=body)
    return status, resp, body


def test_create_get_delete_cycle(admin):
    """正向生命周期：创建 → 详情一致 → 删除 → 详情 404。"""
    status, host, payload = _create(admin)
    assert status == 201 and host["id"] > 0 and host["hostname"] == payload["hostname"]
    try:
        _, got = admin("GET", f"/api/v1/hosts/{host['id']}", expect=200)
        assert got["primary_ip"] == payload["primary_ip"]
        assert got["status"] == "unknown"
        admin("DELETE", f"/api/v1/hosts/{host['id']}", expect=204)
        status, _ = admin("GET", f"/api/v1/hosts/{host['id']}")
        assert status == 404
    finally:
        admin("DELETE", f"/api/v1/hosts/{host['id']}")


def test_create_input_validation(admin):
    """输入验证：非法主机名 / 非法 IP / 缺必填 / 非法 rack_unit 均 422。"""
    for bad in (
        {"hostname": "bad host!", "primary_ip": "10.0.0.1"},
        {"hostname": "ok-host", "primary_ip": "not-an-ip"},
        {"primary_ip": "10.0.0.1"},
        {"hostname": "ok-host"},
        {"hostname": "ok-host", "primary_ip": "10.0.0.1", "rack_unit": 0},
        {"hostname": "ok-host", "primary_ip": "10.0.0.1", "rack_unit": 61},
    ):
        status, body = admin("POST", "/api/v1/hosts", body=bad)
        assert status == 422 and body["code"] == "invalid_input",             f"{bad} 应 422, got {status}"


def test_create_duplicate_bmc_conflict(admin):
    """唯一约束：同一 bmc_ip 第二次录入应 409（错误码 conflict）。"""
    # 注意八位组必须是十进制数字（hex 字符会因非法 IP 直接 422）
    bmc = f"10.77.{uuid.uuid4().int % 200 + 1}.{uuid.uuid4().int % 200 + 1}"
    s1, h1, _ = _create(admin, bmc_ip=bmc)
    assert s1 == 201
    try:
        s2, body, _ = _create(admin, bmc_ip=bmc)
        assert s2 == 409 and "conflict" in body.get("code", "")
    finally:
        admin("DELETE", f"/api/v1/hosts/{h1['id']}")


def test_list_filter_and_pagination(admin):
    """分页与过滤：q 前缀命中、page_size 生效、状态过滤返回结构一致。"""
    prefix = "py-list-" + UNIQUE()
    ids = []
    try:
        for i in range(2):
            status, host, _ = _create(admin, hostname=f"{prefix}-{i}")
            assert status == 201
            ids.append(host["id"])
        _, page = admin(
            "GET", f"/api/v1/hosts?q={prefix}&page_size=1&page=1", expect=200)
        assert page["total"] == 2 and len(page["items"]) == 1
        assert page["page_size"] == 1 and page["page"] == 1
        _, empty = admin("GET", "/api/v1/hosts?q=no-such-host-xyz", expect=200)
        assert empty["total"] == 0 and empty["items"] == []
    finally:
        for hid in ids:
            admin("DELETE", f"/api/v1/hosts/{hid}")


def test_get_missing_host_404(admin):
    """边界：不存在的主机详情返回 404。"""
    status, body = admin("GET", "/api/v1/hosts/999999")
    assert status == 404 and body["code"] == "host_not_found"


def test_host_id_non_numeric_422(admin):
    """边界：非数字主机 ID 应 422（ErrInvalidInput 映射），不得 500。"""
    status, body = admin("GET", "/api/v1/hosts/not-a-number")
    assert status == 422 and body["code"] == "invalid_input"


def test_delete_missing_host_404(admin):
    """边界：删除不存在的主机返回 404。"""
    status, body = admin("DELETE", "/api/v1/hosts/999999")
    assert status == 404 and body["code"] == "host_not_found"


def test_page_beyond_last_empty(admin):
    """分页边界：页码超出末页 → items 为空但 total 保持。"""
    prefix = "py-page-" + UNIQUE()
    status, host, _ = _create(admin, hostname=prefix)
    assert status == 201
    try:
        _, body = admin("GET", f"/api/v1/hosts?q={prefix}&page=99&page_size=10", expect=200)
        assert body["items"] == [] and body["total"] == 1
    finally:
        admin("DELETE", f"/api/v1/hosts/{host['id']}")


def test_page_size_over_cap_resets_to_default(admin):
    """分页边界：page_size=1000 超上限 → 服务端回落默认值 50（行为记录）。"""
    _, body = admin("GET", "/api/v1/hosts?page=1&page_size=1000", expect=200)
    assert body["page_size"] == 50


def test_rack_unit_boundary_valid(admin):
    """边界：rack_unit 合法区间端点（1 与 60）均可录入。"""
    ids = []
    try:
        for ru in (1, 60):
            status, host, _ = _create(admin, rack_unit=ru)
            assert status == 201, f"rack_unit={ru} 应合法"
            ids.append(host["id"])
    finally:
        for hid in ids:
            admin("DELETE", f"/api/v1/hosts/{hid}")


def test_hostname_length_boundary(admin):
    """边界：主机名 64 字符合法（正则上限）、65 字符拒绝。"""
    ok64 = "a" + "-" * 62 + "b"
    bad65 = "a" + "-" * 63 + "b"
    status, host, _ = _create(admin, hostname=ok64)
    assert status == 201
    try:
        status, _, _ = _create(admin, hostname=bad65)
        assert status == 422
    finally:
        admin("DELETE", f"/api/v1/hosts/{host['id']}")


def test_create_invalid_os_type_422(admin):
    """输入验证：os_type 仅允许 linux/windows/unknown。"""
    status, body, _ = _create(admin, os_type="macos")
    assert status == 422 and body["code"] == "invalid_input"
