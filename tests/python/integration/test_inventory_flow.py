"""集成测试 · 资产快照与变更检测（W2/W18）：

经 gRPC 流上行 AssetSnapshot（Go 探针子进程）→ 指纹 diff →
部件树落库 → D11 移除去抖 → removed 变更 + asset_change 告警回链。

探针时间轴：基线 → 重复（无变更）→ 缺席 1（只标记）→ 缺席 2（确认 removed）。
槽位名每次运行唯一，保证用例可重复执行。
"""
import subprocess
import time
import uuid

import pytest
from mw import client, config, db


def _go_available():
    try:
        subprocess.run(["go", "version"], capture_output=True, check=True)
        return True
    except (OSError, subprocess.CalledProcessError):
        return False


@pytest.fixture()
def enrolled_host(admin):
    code = db.seed_enroll_code("MW-PY-" + uuid.uuid4().hex[:12])
    status, resp = client.call("POST", "/api/v1/agent/enroll", body={
        "enroll_code": code, "hostname": "py-inv-" + uuid.uuid4().hex[:8],
        "primary_ip": "10.83.0.1", "smbios_uuid": "py-inv-" + uuid.uuid4().hex[:10],
        "os_type": "linux", "agent_version": "py-integration"})
    assert status == 201, resp
    yield resp["host_id"], resp["agent_token"]
    admin("DELETE", f"/api/v1/hosts/{resp['host_id']}")


@pytest.mark.skipif(not _go_available(), reason="需要 Go 工具链运行 gRPC 探针")
def test_asset_snapshot_change_flow(admin, enrolled_host):
    host_id, token = enrolled_host
    slot = "DIMM_T" + uuid.uuid4().hex[:8]  # 唯一槽位：本用例的变更事件不与其他运行串扰
    base_ms = int(time.time() * 1000) - 10 * 60 * 1000

    proc = subprocess.run(
        ["go", "run", "./devtools/streamprobe",
         "-server", config.BASE_URL, "-token", token,
         "-host", str(host_id), "-slot", slot, "-base", str(base_ms)],
        # check=False：退出码由下面显式断言，这里要让 stderr 能被读出来
        cwd=config.REPO_ROOT / "backend", capture_output=True, text=True,
        timeout=60, check=False)
    assert proc.returncode == 0, f"探针失败: {proc.stderr}"

    # 部件树：基线部件在场，探针内存条已确认移除
    _, comps = admin("GET", f"/api/v1/hosts/{host_id}/components", expect=200)
    slots = {c["slot"] for c in comps}
    assert "CPU1" in slots and "DIMM_A1" in slots and "bios" in slots
    assert slot not in slots, "确认移除的部件不应在役"

    # 变更记录：恰好一条 removed，且字段契约对齐前端 ChangeRecord
    _, changes = admin("GET", f"/api/v1/hosts/{host_id}/changes", expect=200)
    removed = [c for c in changes if c["change_type"] == "removed" and c["slot"] == slot]
    assert len(removed) == 1, f"应恰好一条 removed: {changes}"
    assert removed[0]["category"] == "memory"
    assert removed[0]["source"] == "agent"
    assert removed[0]["detected_at"]

    # W18：变更回链 asset_change（info）告警（回链 ID 在库表核对，
    # REST ChangeRecord 契约不含该内部字段）
    rows = db.alerts_for_host(host_id, category="asset_change")
    assert rows and rows[0]["severity"] == "info"
    link = db.query(
        "SELECT alert_event_id FROM change_event WHERE host_id = ? AND slot = ? AND change_type = 'removed'",
        (host_id, slot))
    assert link and link[0]["alert_event_id"] == rows[0]["id"]
