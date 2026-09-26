"""用例档案注册表：每个测试用例的正式 ID、前置条件与预期结果（D34 配套资产）。

键 = (模块名, 用例名)；objective 取用例 docstring（单一来源，不在此重复）。
执行后由 run_tests.py 合并实际步骤（HTTP 报文捕获）生成干系人级详细报告。
新增用例必须同步登记 ID（评审清单项，见 D34）。
"""

# 前置条件通用写法（组合复用）
P_SERVER = "本地服务端已启动并完成引导初始化（healthz=ok）"
P_ADMIN = P_SERVER + "；已持有管理员会话令牌"
P_AGENT = P_SERVER + "；本机 Agent 已注册并在线（host_id=1）"

CASES = {
    # ================= 冒烟（TC-SMK） =================
    ("test_smoke", "test_01_healthz"): {"id": "TC-SMK-001", "pre": P_SERVER},
    ("test_smoke", "test_02_login_and_me"): {"id": "TC-SMK-002", "pre": P_SERVER},
    ("test_smoke", "test_03_unauthorized_rejected"): {"id": "TC-SMK-003", "pre": P_SERVER},
    ("test_smoke", "test_04_hosts_list"): {"id": "TC-SMK-004", "pre": P_ADMIN},
    ("test_smoke", "test_05_overview"): {"id": "TC-SMK-005", "pre": P_ADMIN},
    ("test_smoke", "test_06_alerts_and_templates"): {"id": "TC-SMK-006", "pre": P_ADMIN},
    ("test_smoke", "test_07_system_status"): {"id": "TC-SMK-007", "pre": P_ADMIN},
    ("test_smoke", "test_08_storage_backends"): {"id": "TC-SMK-008", "pre": P_ADMIN},
    ("test_smoke", "test_09_agent_online"): {"id": "TC-SMK-009", "pre": P_AGENT},
    ("test_smoke", "test_10_frontend_reachable"): {"id": "TC-SMK-010", "pre": "前端 dev server 已启动"},

    # ================= 鉴权（TC-AUTH） =================
    ("test_auth", "test_login_wrong_password_401"): {
        "id": "TC-AUTH-001", "pre": P_SERVER,
        "expected": "HTTP 401，code=bad_credential；连续失败会触发防爆破门槛（本用例不触发）"},
    ("test_auth", "test_login_missing_fields_422"): {
        "id": "TC-AUTH-002", "pre": P_SERVER,
        "expected": "HTTP 4xx（缺 password 的 JSON 绑定失败），不得 500"},
    ("test_auth", "test_me_returns_admin"): {
        "id": "TC-AUTH-003", "pre": P_ADMIN, "expected": "HTTP 200，username=admin、role=admin"},
    ("test_auth", "test_me_without_token_401"): {
        "id": "TC-AUTH-004", "pre": P_SERVER, "expected": "HTTP 401，code=unauthorized"},
    ("test_auth", "test_logout_ok"): {
        "id": "TC-AUTH-005", "pre": P_ADMIN, "expected": 'HTTP 200，{"ok":true}，且审计落库'},
    ("test_auth", "test_change_password_wrong_old_401"): {
        "id": "TC-AUTH-006", "pre": P_ADMIN,
        "expected": "HTTP 401 bad_credential；管理员原口令不受影响（仍可登录）"},
    ("test_auth", "test_audit_logs_record_login"): {
        "id": "TC-AUTH-007", "pre": P_ADMIN,
        "expected": "GET /audit-logs?action=auth.login 可查到登录成功审计记录"},
    ("test_auth", "test_login_empty_username_4xx"): {
        "id": "TC-AUTH-008", "pre": P_SERVER, "expected": "HTTP 4xx，不得 500"},
    ("test_auth", "test_audit_logs_filter_by_result"): {
        "id": "TC-AUTH-009", "pre": P_ADMIN,
        "expected": "HTTP 200；result=denied 过滤后所有行 result 均为 denied"},

    # ================= 资产（TC-HOST） =================
    ("test_hosts", "test_create_get_delete_cycle"): {
        "id": "TC-HOST-001", "pre": P_ADMIN,
        "expected": "创建 201（status=unknown）→ 详情一致 → 删除 204 → 详情 404"},
    ("test_hosts", "test_create_input_validation"): {
        "id": "TC-HOST-002", "pre": P_ADMIN,
        "expected": "非法主机名/非法 IP/缺 hostname/缺 primary_ip/rack_unit=0/61 均 422 invalid_input"},
    ("test_hosts", "test_create_duplicate_bmc_conflict"): {
        "id": "TC-HOST-003", "pre": P_ADMIN, "expected": "同一 bmc_ip 第二次录入 409 conflict"},
    ("test_hosts", "test_list_filter_and_pagination"): {
        "id": "TC-HOST-004", "pre": P_ADMIN,
        "expected": "q 前缀命中 total=2；page_size=1 生效；不匹配 q 返回空集"},
    ("test_hosts", "test_get_missing_host_404"): {
        "id": "TC-HOST-005", "pre": P_ADMIN, "expected": "HTTP 404 host_not_found"},
    ("test_hosts", "test_host_id_non_numeric_422"): {
        "id": "TC-HOST-006", "pre": P_ADMIN,
        "expected": "HTTP 422 invalid_input（非数字 ID 拒绝），不得 500"},
    ("test_hosts", "test_delete_missing_host_404"): {
        "id": "TC-HOST-007", "pre": P_ADMIN, "expected": "HTTP 404 host_not_found"},
    ("test_hosts", "test_page_beyond_last_empty"): {
        "id": "TC-HOST-008", "pre": P_ADMIN, "expected": "第 99 页 items=[] 且 total=1"},
    ("test_hosts", "test_page_size_over_cap_resets_to_default"): {
        "id": "TC-HOST-009", "pre": P_ADMIN,
        "expected": "page_size=1000 超上限 → 响应 page_size=50（回落默认，行为记录）"},
    ("test_hosts", "test_rack_unit_boundary_valid"): {
        "id": "TC-HOST-010", "pre": P_ADMIN, "expected": "rack_unit=1 与 60（区间端点）均 201"},
    ("test_hosts", "test_hostname_length_boundary"): {
        "id": "TC-HOST-011", "pre": P_ADMIN, "expected": "64 字符主机名 201；65 字符 422"},
    ("test_hosts", "test_create_invalid_os_type_422"): {
        "id": "TC-HOST-012", "pre": P_ADMIN,
        "expected": "os_type=macos → 422 invalid_input（仅 linux/windows/unknown）"},

    # ================= Agent 协议（TC-AGT） =================
    ("test_agent_protocol", "test_enroll_bad_code_401"): {
        "id": "TC-AGT-001", "pre": P_SERVER, "expected": "HTTP 401 enroll_code_used"},
    ("test_agent_protocol", "test_enroll_invalid_input_422"): {
        "id": "TC-AGT-002", "pre": P_SERVER + "；已种子注册码",
        "expected": "HTTP 422（primary_ip 非法）"},
    ("test_agent_protocol", "test_enroll_once_then_reuse_denied"): {
        "id": "TC-AGT-003", "pre": P_SERVER + "；已种子一次性注册码",
        "expected": "首次 201；同码再注册 401（一次性语义，M4 验收 1）"},
    ("test_agent_protocol", "test_report_requires_token"): {
        "id": "TC-AGT-004", "pre": P_ADMIN + "；已注册测试主机", "expected": "HTTP 401"},
    ("test_agent_protocol", "test_report_forbidden_scope"): {
        "id": "TC-AGT-005", "pre": P_ADMIN + "；已注册测试主机",
        "expected": "HTTP 403 forbidden_scope（host_id 与令牌不符，M4 验收）"},
    ("test_agent_protocol", "test_report_metric_whitelist"): {
        "id": "TC-AGT-006", "pre": P_ADMIN + "；已注册测试主机",
        "expected": "HTTP 422 metric_not_allowed（未知指标 cpu_usage_percent）"},
    ("test_agent_protocol", "test_report_high_cardinality_label"): {
        "id": "TC-AGT-007", "pre": P_ADMIN + "；已注册测试主机",
        "expected": "HTTP 422（sn 标签被高基数黑名单拒绝）"},
    ("test_agent_protocol", "test_report_backfill_window"): {
        "id": "TC-AGT-008", "pre": P_ADMIN + "；已注册测试主机",
        "expected": "HTTP 422 timestamp_out_of_range（collected_at 早于 24h）"},
    ("test_agent_protocol", "test_report_requires_batch_id"): {
        "id": "TC-AGT-009", "pre": P_ADMIN + "；已注册测试主机", "expected": "HTTP 422"},
    ("test_agent_protocol", "test_report_accept_and_replay_dedup"): {
        "id": "TC-AGT-010", "pre": P_ADMIN + "；已注册测试主机",
        "expected": "首次 202 accepted=1/tsdb=accepted；重放 202 accepted=0/tsdb=deduplicated（M3 验收 2）"},
    ("test_agent_protocol", "test_heartbeat_ok"): {
        "id": "TC-AGT-011", "pre": P_ADMIN + "；已注册测试主机",
        "expected": "HTTP 200 ok=true 且 report_interval_sec>0"},
    ("test_agent_protocol", "test_enroll_empty_uuid_creates_host"): {
        "id": "TC-AGT-012", "pre": P_SERVER + "；已种子注册码",
        "expected": "空 smbios_uuid 两次注册得到两个不同 host_id（无可复用指纹）"},
    ("test_agent_protocol", "test_report_empty_metrics_accepted"): {
        "id": "TC-AGT-013", "pre": P_ADMIN + "；已注册测试主机", "expected": "HTTP 202 accepted=0"},
    ("test_agent_protocol", "test_report_protobuf_content_type_501"): {
        "id": "TC-AGT-014", "pre": P_ADMIN + "；已注册测试主机",
        "expected": "Content-Type=x-protobuf → HTTP 501 protobuf_not_enabled（不得误解析）"},
    ("test_agent_protocol", "test_report_wrong_method_404"): {
        "id": "TC-AGT-015", "pre": P_ADMIN + "；已注册测试主机", "expected": "GET 上报端点 → HTTP 404"},
    ("test_agent_protocol", "test_report_invalid_json_4xx"): {
        "id": "TC-AGT-016", "pre": P_ADMIN + "；已注册测试主机",
        "expected": "HTTP 4xx（非法 JSON 体），不得 500"},

    # ================= 时序查询（TC-MET） =================
    ("test_metrics", "test_metric_required_422"): {
        "id": "TC-MET-001", "pre": P_ADMIN, "expected": "HTTP 422 invalid_input（缺 metric）"},
    ("test_metrics", "test_bad_time_format_422"): {
        "id": "TC-MET-002", "pre": P_ADMIN, "expected": "HTTP 422（from 非 RFC3339）"},
    ("test_metrics", "test_from_not_before_to_422"): {
        "id": "TC-MET-003", "pre": P_ADMIN, "expected": "HTTP 422（from ≥ to）"},
    ("test_metrics", "test_bad_step_422"): {
        "id": "TC-MET-004", "pre": P_ADMIN, "expected": "HTTP 422（step=abc）"},
    ("test_metrics", "test_too_small_step_422"): {
        "id": "TC-MET-005", "pre": P_ADMIN, "expected": "HTTP 422（step=10ms < 1s 下限）"},
    ("test_metrics", "test_up_metric_curve"): {
        "id": "TC-MET-006", "pre": P_AGENT,
        "expected": "HTTP 200；metric=up、source=embedded、points 非空且最新值=1"},
    ("test_metrics", "test_labels_filter_no_match"): {
        "id": "TC-MET-007", "pre": P_ADMIN, "expected": "HTTP 200 且 points=[]（无匹配不报错）"},
    ("test_metrics", "test_metric_nonexistent_host_empty"): {
        "id": "TC-MET-008", "pre": P_ADMIN,
        "expected": "HTTP 200 且 points=[]（不校验主机存在性——W1c 设计取舍，行为记录）"},
    ("test_metrics", "test_metric_future_range_empty"): {
        "id": "TC-MET-009", "pre": P_ADMIN, "expected": "HTTP 200 且 points=[]（区间全在未来）"},
    ("test_metrics", "test_metric_long_range_downsampled"): {
        "id": "TC-MET-010", "pre": P_AGENT,
        "expected": "HTTP 200 且 downsampled=true（>24h 区间自动切聚合档/降采样）"},

    # ================= 告警中心（TC-ALM） =================
    ("test_alerts", "test_templates_shape"): {
        "id": "TC-ALM-001", "pre": P_ADMIN,
        "expected": "内置模板 ≥6 条；字段齐全（id/name/metric/op/threshold/severity/for_duration/enabled）"},
    ("test_alerts", "test_alerts_state_validation"): {
        "id": "TC-ALM-002", "pre": P_ADMIN, "expected": "HTTP 422（state=bogus）"},
    ("test_alerts", "test_alerts_list_all_shape"): {
        "id": "TC-ALM-003", "pre": P_ADMIN, "expected": "HTTP 200 列表；元素字段符合前端 Alert 契约"},
    ("test_alerts", "test_ack_missing_alert_404"): {
        "id": "TC-ALM-004", "pre": P_ADMIN, "expected": "HTTP 404（确认不存在的告警）"},
    ("test_alerts", "test_state_filters_disjoint"): {
        "id": "TC-ALM-005", "pre": P_ADMIN, "expected": "active 与 resolved 查询结果 ID 无交集"},
    ("test_alerts", "test_alerts_severity_filter"): {
        "id": "TC-ALM-006", "pre": P_ADMIN,
        "expected": "HTTP 200 空列表（severity=bogus 为未知级别，行为记录）"},
    ("test_alerts", "test_alerts_host_id_non_numeric_ignored"): {
        "id": "TC-ALM-007", "pre": P_ADMIN,
        "expected": "HTTP 200（host_id=abc 被忽略，不过滤）"},

    # ================= 带外管理（TC-BMC） =================
    ("test_bmc", "test_put_bmc_input_validation"): {
        "id": "TC-BMC-001", "pre": P_ADMIN + "；已创建临时主机",
        "expected": "非法 IP/缺密码/非法协议均 422"},
    ("test_bmc", "test_credential_encrypted_at_rest"): {
        "id": "TC-BMC-002", "pre": P_ADMIN + "；已创建临时主机",
        "expected": "凭据落库：密文/nonce 非空，且明文不出现在 secret_cipher（D9 验收）"},
    ("test_bmc", "test_bmc_test_unreachable"): {
        "id": "TC-BMC-003", "pre": P_ADMIN + "；已录入凭据（本机无 BMC）",
        "expected": "HTTP 502 bmc_unreachable（链路走通、错误如实返回，非 500）"},
    ("test_bmc", "test_delete_credential"): {
        "id": "TC-BMC-004", "pre": P_ADMIN + "；已录入凭据",
        "expected": "删除 204 且库中行清除；重复删除 404"},
    ("test_bmc", "test_collect_runs_endpoint"): {
        "id": "TC-BMC-005", "pre": P_ADMIN, "expected": "HTTP 200，items/total 结构完整"},
    ("test_bmc", "test_put_bmc_missing_host_404"): {
        "id": "TC-BMC-006", "pre": P_ADMIN,
        "expected": "HTTP 404（不存在的主机录入凭据，经 service 层 404 语义）"},
    ("test_bmc", "test_put_bmc_ipv6_and_redfish"): {
        "id": "TC-BMC-007", "pre": P_ADMIN + "；已创建临时主机",
        "expected": "IPv6 地址 + redfish 协议录入 204；库中 protocol=redfish"},

    # ================= 开放令牌（TC-TOK） =================
    ("test_tokens", "test_token_lifecycle"): {
        "id": "TC-TOK-001", "pre": P_ADMIN,
        "expected": "创建返回 mwo_ 明文（仅一次）→ 列表可见（无明文、state=active）→ 吊销后 state=revoked"},
    ("test_tokens", "test_token_create_requires_auth"): {
        "id": "TC-TOK-002", "pre": P_SERVER, "expected": "HTTP 401"},
    ("test_tokens", "test_token_create_with_scopes_and_expiry"): {
        "id": "TC-TOK-003", "pre": P_ADMIN,
        "expected": "自定义 scopes=asset:read、expire_days=7 创建成功；列表回显 scopes 与 expire_at"},

    # ================= 系统状态 / WS（TC-SYS / TC-WS） =================
    # 注意：test_system_status_shape 与 WS 用例同文件（test_ws.py），按文件登记
    ("test_ws", "test_system_status_shape"): {
        "id": "TC-SYS-001", "pre": P_ADMIN,
        "expected": "version/uptime/storage/host_count/collect 字段齐全，schema_version ≥ 1"},
    ("test_ws", "test_ws_requires_auth"): {
        "id": "TC-WS-001", "pre": P_SERVER, "expected": "无令牌握手被拒（HTTP 401，不升级协议）"},
    ("test_ws", "test_ws_handshake_and_accept"): {
        "id": "TC-WS-002", "pre": P_ADMIN,
        "expected": "握手 101 且 Sec-WebSocket-Accept 按 RFC6455 正确；空闲期无乱推帧"},

    # ================= 集成（TC-INT） =================
    ("test_data_flow", "test_report_query_dedup_flow"): {
        "id": "TC-INT-001", "pre": P_SERVER + "；已种子注册码与全新主机",
        "expected": "上报两批 → fan_rpm 曲线出齐 2 点（值=1200）→ 标签过滤命中 → 批次重放幂等"},
    ("test_data_flow", "test_enroll_then_host_visible"): {
        "id": "TC-INT-002", "pre": P_SERVER + "；已种子注册码",
        "expected": "注册即建资产：collect_agent=true，状态 online/unknown"},
    ("test_alert_flow", "test_alert_full_cycle"): {
        "id": "TC-INT-003", "pre": P_SERVER + "；独立注册主机 + WS 订阅 + webhook 渠道（secret=py-test-secret，门槛 info）",
        "expected": "92℃ 持续 → critical firing → WS 收 alert.firing → Webhook 收 alert.firing 且 HMAC 验签一致 → notify_state=sent → ack → 三周期恢复 → resolved（WS+Webhook）→ 故障史保留"},
    ("test_inventory_flow", "test_asset_snapshot_change_flow"): {
        "id": "TC-INT-004", "pre": P_SERVER + "；独立注册主机；Go 工具链可运行 gRPC 探针",
        "expected": "探针时间轴（基线→重复→缺席×2）→ 部件树在役 3 件不含探针内存条 → 恰 1 条 removed → 回链 asset_change(info) 告警"},
    ("test_offline_flow", "test_offline_detection_and_recovery"): {
        "id": "TC-INT-005", "pre": P_AGENT + "；Agent 二进制与令牌文件在位",
        "expected": "停 Agent → 置 offline + agent_offline(info) 触发 + 幂等不重复 → 重启 Agent → 恢复 online + 自动 resolved → 故障史保留"},
}


def get(module: str, name: str) -> dict:
    """查用例档案；未登记的返回默认档案（保证报告不缺项）。"""
    meta = CASES.get((module, name))
    if meta is None:
        return {"id": "TC-UNREG-000", "pre": P_SERVER,
                    "note": "⚠ 未在 caseregistry 登记（D34 违规）"}
    return meta
