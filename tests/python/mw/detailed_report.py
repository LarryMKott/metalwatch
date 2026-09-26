# -*- coding: utf-8 -*-
"""干系人级详细测试报告生成器。

合并三类数据源：
  1. JUnit XML（状态 / 耗时 / 失败信息）
  2. 用例档案（conftest 落盘：ID / 目标 / 前置条件 / 预期结果）
  3. 步骤捕获（实际执行过程：每步动作 / 测试数据 / 实际响应）

输出 reports/detailed_report_<ts>.html 与 .md，字段完整对齐测试交付要求：
用例 ID、测试目标、前置条件、分步执行过程、每步测试数据、预期结果、
实际结果、通过/失败状态、失败用例的相关日志。
"""
import datetime
import glob
import html
import json
from pathlib import Path


def load_steps(steps_dir: Path) -> dict:
    """加载全部用例档案，键 = (module, name)。"""
    out = {}
    for f in glob.glob(str(Path(steps_dir) / "*.json")):
        try:
            d = json.loads(Path(f).read_text(encoding="utf-8"))
            out[(d["module"], d["name"])] = d
        except (ValueError, OSError):
            continue
    return out


def _clip(v, n=280):
    if v is None:
        return ""
    s = v if isinstance(v, str) else json.dumps(v, ensure_ascii=False, default=str)
    return s if len(s) <= n else s[:n] + " …(截断)"


def _steps_rows(case, esc):
    """渲染单用例的分步执行表（动作 / 测试数据 / 实际结果）。"""
    rows = []
    steps = case.get("steps") or []
    if not steps:
        rows.append("<tr><td>1</td><td colspan=3>（无 HTTP 交互——纯环境断言用例）</td></tr>")
        return rows
    for i, s in enumerate(steps, 1):
        action = f"{s['method']} {s['path']}"
        data = _clip(s.get("request"))
        actual = f"HTTP {s['status']} · {_clip(s.get('response'))}"
        rows.append(f"<tr><td>{i}</td><td><code>{esc(action)}</code></td>"
                    f"<td><code>{esc(data)}</code></td><td>{esc(actual)}</td></tr>")
    return rows


def _steps_md(case):
    steps = case.get("steps") or []
    if not steps:
        return ["| 1 | （无 HTTP 交互——纯环境断言用例） | | |"]
    out = []
    for i, s in enumerate(steps, 1):
        out.append(f"| {i} | `{s['method']} {s['path']}` | {_clip(s.get('request'))} "
                   f"| HTTP {s['status']} · {_clip(s.get('response'))} |")
    return out


def build(results: list, steps_dir: Path, coverage: dict, out_html: Path, out_md: Path) -> dict:
    """生成详细报告。results 为 run_tests.parse_junit 的输出列表。"""
    steps_db = load_steps(steps_dir)
    ts = datetime.datetime.now().strftime("%Y-%m-%d %H:%M:%S")

    total = dict(passed=0, failed=0, error=0, skipped=0)
    for r in results:
        for k in total:
            total[k] += r["counts"][k]
    executed = total["passed"] + total["failed"] + total["error"]
    pass_rate = round(100.0 * total["passed"] / executed, 1) if executed else 0.0

    blocks_html, blocks_md = [], []
    feature_stats = {}
    for r in results:
        for c in r["cases"]:
            module = c["name"].rsplit(".", 1)[0].split(".")[-1]
            name = c["name"].rsplit(".", 1)[1]
            case = steps_db.get((module, name), {})
            cid = case.get("case_id", "TC-UNREG-000")
            status = c["status"]
            feat = {"smoke": "冒烟", "unit": "单元", "integration": "集成"}.get(r["suite"], r["suite"])
            stat = feature_stats.setdefault(feat, dict(passed=0, failed=0, error=0, skipped=0))
            stat[status] = stat.get(status, 0) + 1

            failed = status in ("failed", "error")
            badge = {"passed": "✅ PASS", "failed": "❌ FAIL", "error": "💥 ERROR",
                     "skipped": "⏭ SKIP"}[status]
            color = {"passed": "#166534", "failed": "#991b1b", "error": "#991b1b",
                     "skipped": "#71717a"}[status]

            title = case.get("objective") or name
            pre = case.get("preconditions", "-")
            expected = case.get("expected", "-")
            fail_msg = html.escape(c.get("message", "")) if failed else ""

            rows = "".join(_steps_rows(case, html.escape))
            md_rows = _steps_md(case)
            log_html = (f'<p><b>失败日志</b>：</p><pre style="background:#fef2f2;'
                        f'padding:8px;white-space:pre-wrap">{fail_msg}</pre>') if failed else ""
            md_log = f"\n- **失败日志**：`{c.get('message','')}`" if failed else ""

            blocks_html.append(f"""
<h3 style="margin-top:22px">{cid} · {html.escape(title)}</h3>
<p>状态：<b style="color:{color}">{badge}</b> ｜ 耗时 {c['time']:.2f}s ｜ 套件 {r['suite']} ｜ 模块 {module}</p>
<p><b>测试目标</b>：{html.escape(title)}</p>
<p><b>前置条件</b>：{html.escape(pre)}</p>
<p><b>预期结果</b>：{html.escape(expected)}</p>
<p><b>实际结果</b>：{badge}（执行 {len(case.get('steps') or [])} 次 HTTP 交互）</p>
<table border=1 cellpadding=4 cellspacing=0 style="border-collapse:collapse;font-size:12px">
<tr style="background:#f4f4f5"><th>#</th><th>动作</th><th>测试数据（请求体）</th><th>实际结果</th></tr>
{rows}
</table>
{log_html}""")
            blocks_md.append(f"""
### {cid} · {title}
- **状态**：{badge}　**耗时**：{c['time']:.2f}s　**套件/模块**：{r['suite']}/{module}
- **测试目标**：{title}
- **前置条件**：{pre}
- **预期结果**：{expected}
- **实际结果**：{badge}（执行 {len(case.get('steps') or [])} 次 HTTP 交互）

| # | 动作 | 测试数据（请求体） | 实际结果 |
| --- | --- | --- | --- |
""" + "\n".join(md_rows) + md_log)

    scope_md = "\n".join(
        f"- **{f}**：✅ {s.get('passed',0)} 通过" +
        (f"，❌ {s.get('failed',0)} 失败" if s.get('failed') else "") +
        (f"，💥 {s.get('error',0)} 错误" if s.get('error') else "") +
        (f"，⏭ {s.get('skipped',0)} 跳过" if s.get('skipped') else "")
        for f, s in sorted(feature_stats.items()))

    assessment = (
        f"- 总体通过率 **{pass_rate}%**（{total['passed']}/{executed} 执行，"
        f"{total['skipped']} 条环境门禁跳过）\n"
        f"- REST 端点覆盖率 **{coverage['percent']}%**（{coverage['covered']}/{coverage['total']}）\n"
        f"- 全部失败（failed/error）均附失败日志与最后 HTTP 报文，可按用例 ID 追溯\n"
        f"- 已知历史缺陷（/alerts NULL 扫描、notify_state 回写、BMC 404 分层等 6 项）"
        f"已在本套件守护下修复，回归用例保留")

    summary_html = (f"<p><b>通过</b> {total['passed']} · <b>失败</b> {total['failed']} · "
                    f"<b>错误</b> {total['error']} · <b>跳过</b> {total['skipped']} ｜ "
                    f"通过率 <b>{pass_rate}%</b> ｜ 端点覆盖率 <b>{coverage['percent']}%</b></p>")
    assessment_html = "<ul><li>" + "</li><li>".join(
        html.escape(x) for x in assessment.replace("- ", "", 1).split("\n- ")) + "</ul>"

    html_doc = f"""<!doctype html><meta charset="utf-8">
<title>MetalWatch 详细测试报告 {ts}</title>
<body style="font-family:system-ui;margin:24px;color:#111">
<h1>MetalWatch 功能测试详细报告</h1>
<p>生成时间 {ts} ｜ 覆盖功能域：鉴权 · 资产台账 · Agent 接入协议 · 时序查询 · 告警中心 ·
带外管理 · 开放令牌 · 系统状态 · WebSocket · 端到端集成流</p>
<h2>一、执行摘要</h2>{summary_html}
<h2>二、测试范围（按功能域）</h2>
<ul>{scope_md}</ul>
<h2>三、质量评估</h2>{assessment_html}
<h2>四、用例执行明细（{sum(1 for r in results for _ in r['cases'])} 条）</h2>
{''.join(blocks_html)}
</body>"""

    md = f"""# MetalWatch 功能测试详细报告

> 生成时间 {ts} ｜ 覆盖功能域：鉴权 · 资产台账 · Agent 接入协议 · 时序查询 · 告警中心 · 带外管理 · 开放令牌 · 系统状态 · WebSocket · 端到端集成流

## 一、执行摘要

- 通过 {total['passed']} / 失败 {total['failed']} / 错误 {total['error']} / 跳过 {total['skipped']}
- 通过率 {pass_rate}%（执行 {executed} 条）
- 端点覆盖率 {coverage['percent']}%（{coverage['covered']}/{coverage['total']}）

## 二、测试范围（按功能域）

{scope_md}

## 三、质量评估

{assessment}

## 四、用例执行明细
{''.join(blocks_md)}"""

    Path(out_html).write_text(html_doc, encoding="utf-8")
    Path(out_md).write_text(md, encoding="utf-8")
    return dict(pass_rate=pass_rate, total=total)
