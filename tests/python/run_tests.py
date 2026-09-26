# -*- coding: utf-8 -*-
"""MetalWatch Python 测试执行器。

用法（在 tests/python/ 目录下）：
    python run_tests.py                # 全量：冒烟 → 单元 → 集成
    python run_tests.py smoke          # 只跑冒烟
    python run_tests.py unit           # 只跑单元
    python run_tests.py integration    # 只跑集成
    python run_tests.py all --fast     # 集成跳过 slow 用例（离线判定）

产出：
    reports/junit_<suite>.xml      JUnit 结果（CI 可直接消费）
    reports/report_<ts>.html       自包含 HTML 报告（结果 + 端点覆盖率）
    reports/latest.md              Markdown 摘要

通知（默认关闭，配置环境变量即启用）：
    邮件：MW_SMTP_HOST / MW_SMTP_PORT / MW_SMTP_USER / MW_SMTP_PASS
          MW_NOTIFY_EMAIL_TO（收件人，逗号分隔）
    Webhook（企业微信/钉钉/飞书/自定义，POST JSON 文本）：
          MW_NOTIFY_WEBHOOK_URL
"""
import argparse
import datetime
import glob
import json
import shutil
import smtplib
import subprocess
import sys
import urllib.request
import xml.etree.ElementTree as ET
from email.mime.text import MIMEText
from pathlib import Path

HERE = Path(__file__).parent
REPORTS = HERE / "reports"
SUITES = ("smoke", "unit", "integration")


def run_suite(name: str, fast: bool, allure_dir: Path = None) -> Path:
    """跑一层测试，返回 JUnit XML 路径。"""
    junit = REPORTS / f"junit_{name}.xml"
    args = [sys.executable, "-m", "pytest", name, "-q",
            "--junitxml", str(junit), "--rootdir", str(HERE)]
    if allure_dir is not None:
        args += ["--alluredir", str(allure_dir)]
    if fast:
        args += ["-m", "not slow"]
    proc = subprocess.run(args, cwd=HERE)
    if not junit.exists():
        raise RuntimeError(f"{name}: 未产生结果文件（pytest 异常退出 {proc.returncode}）")
    return junit


def parse_junit(xml: Path) -> dict:
    """解析 JUnit XML 为汇总结构。"""
    root = ET.parse(xml).getroot()
    suite = root.find("testsuite") if root.tag == "testsuites" else root
    cases = []
    for case in suite.iter("testcase"):
        item = {"name": f"{case.get('classname','')}.{case.get('name','')}",
                "time": float(case.get("time", 0)), "status": "passed"}
        for tag, label in (("failure", "failed"), ("error", "error"), ("skipped", "skipped")):
            if case.find(tag) is not None:
                item["status"] = label
                node = case.find(tag)
                item["message"] = (node.get("message") or "")[:300]
        cases.append(item)
    counts = {"passed": 0, "failed": 0, "error": 0, "skipped": 0}
    for c in cases:
        counts[c["status"]] += 1
    return {"suite": xml.stem.replace("junit_", ""),
            "cases": cases, "counts": counts,
            "time": round(sum(c["time"] for c in cases), 2)}


def coverage_section() -> str:
    """端点覆盖率段落（表格 + 未覆盖清单）。"""
    from mw import endpoints  # noqa: E402  依赖 repo 布局，延迟导入
    s = endpoints.summary(REPORTS / "coverage_hits.json")
    lines = [f"**端点覆盖率：{s['percent']}%**（{s['covered']}/{s['total']}）"]
    if s["missing"]:
        lines.append("\n未覆盖端点：")
        lines += [f"- `{m}`" for m in s["missing"]]
    return "\n".join(lines)


def build_report(results: list) -> tuple[Path, Path]:
    """生成 HTML 与 Markdown 报告，返回路径。"""
    ts = datetime.datetime.now().strftime("%Y%m%d_%H%M%S")
    total = dict(passed=0, failed=0, error=0, skipped=0)
    seconds = 0.0
    for r in results:
        for k in total:
            total[k] += r["counts"][k]
        seconds += r["time"]

    cov = coverage_section()
    md = [f"# MetalWatch 测试报告 {ts}",
          f"\n总计：✅ {total['passed']} 通过 · ❌ {total['failed']} 失败 · "
          f"💥 {total['error']} 错误 · ⏭ {total['skipped']} 跳过 · {seconds:.1f}s\n"]
    for r in results:
        md.append(f"\n## {r['suite']}（{r['counts']['passed']} 通过 / "
                  f"{r['counts']['failed']} 失败 / {r['counts']['error']} 错误 / "
                  f"{r['counts']['skipped']} 跳过，{r['time']}s）\n")
        for c in r["cases"]:
            icon = {"passed": "✅", "failed": "❌", "error": "💥", "skipped": "⏭"}[c["status"]]
            line = f"- {icon} `{c['name']}`"
            if c["status"] != "passed":
                line += f" —— {c.get('message','')}"
            md.append(line)
    md.append("\n## 端点覆盖率\n\n" + cov)
    md_text = "\n".join(md)

    md_path = REPORTS / "latest.md"
    md_path.write_text(md_text, encoding="utf-8")

    rows_html = ""
    for r in results:
        for c in r["cases"]:
            color = {"passed": "#166534", "failed": "#991b1b",
                     "error": "#991b1b", "skipped": "#71717a"}[c["status"]]
            msg = c.get("message", "").replace("<", "&lt;")
            rows_html += (f"<tr><td>{r['suite']}</td><td><code>{c['name']}</code></td>"
                          f"<td style='color:{color}'>{c['status']}</td>"
                          f"<td>{c['time']:.2f}s</td><td>{msg}</td></tr>")
    cov_lines = cov.replace("\n", "<br>").replace("**", "<b>")
    html = f"""<!doctype html><meta charset="utf-8">
<title>MetalWatch 测试报告 {ts}</title>
<body style="font-family:system-ui;margin:24px;color:#111">
<h1>MetalWatch 测试报告 <small style="color:#888">{ts}</small></h1>
<p>✅ {total['passed']} 通过 · ❌ {total['failed']} 失败 · 💥 {total['error']} 错误 ·
   ⏭ {total['skipped']} 跳过 · {seconds:.1f}s</p>
<h2>用例</h2>
<table border=1 cellpadding=6 cellspacing=0 style="border-collapse:collapse;font-size:13px">
<tr style="background:#f4f4f5"><th>套件</th><th>用例</th><th>结果</th><th>耗时</th><th>失败详情</th></tr>
{rows_html}
</table>
<h2>端点覆盖率</h2><div style="font-size:13px">{cov_lines}</div>
</body>"""
    html_path = REPORTS / f"report_{ts}.html"
    html_path.write_text(html, encoding="utf-8")
    return html_path, md_path


def notify(md_path: Path, total: dict) -> None:
    """结果通知：邮件与 Webhook 均按环境变量启用，未配置则跳过。"""
    text = md_path.read_text(encoding="utf-8")
    subject = (f"[MetalWatch 测试] 通过 {total['passed']} / 失败 {total['failed']} / "
               f"错误 {total['error']}")
    smtp_host = __import__("os").environ.get("MW_SMTP_HOST")
    if smtp_host:
        try:
            msg = MIMEText(text, "plain", "utf-8")
            msg["Subject"] = subject
            msg["From"] = __import__("os").environ.get("MW_SMTP_USER", "metalwatch@localhost")
            to = __import__("os").environ["MW_NOTIFY_EMAIL_TO"]
            msg["To"] = to
            with smtplib.SMTP(smtp_host, int(__import__("os").environ.get("MW_SMTP_PORT", 25))) as s:
                if __import__("os").environ.get("MW_SMTP_USER"):
                    s.starttls()
                    s.login(__import__("os").environ["MW_SMTP_USER"],
                            __import__("os").environ.get("MW_SMTP_PASS", ""))
                s.send_message(msg)
            print(f"通知：邮件已发送至 {to}")
        except Exception as e:  # 通知失败不影响测试结论
            print(f"通知：邮件发送失败（{e}）")
    hook_url = __import__("os").environ.get("MW_NOTIFY_WEBHOOK_URL")
    if hook_url:
        try:
            body = json.dumps({"msgtype": "text",
                               "text": {"content": subject + "\n" + text[:1500]}}).encode()
            req = urllib.request.Request(hook_url, data=body,
                                         headers={"Content-Type": "application/json"})
            urllib.request.urlopen(req, timeout=10)
            print("通知：Webhook 已推送")
        except Exception as e:
            print(f"通知：Webhook 推送失败（{e}）")
    if not smtp_host and not hook_url:
        print("通知：未配置（MW_SMTP_* / MW_NOTIFY_WEBHOOK_URL）")


def main():
    ap = argparse.ArgumentParser(description="MetalWatch Python 测试执行器")
    ap.add_argument("suites", nargs="*",
                    help="smoke / unit / integration / all（缺省 = all）")
    ap.add_argument("--fast", action="store_true", help="集成跳过 slow 用例")
    ap.add_argument("--no-allure", action="store_true",
                    help="关闭 Allure 结果生成（默认开启，需 allure-pytest）")
    args = ap.parse_args()
    names = SUITES if not args.suites or "all" in args.suites else args.suites
    unknown = set(names) - set(SUITES)
    if unknown:
        ap.error(f"未知套件: {sorted(unknown)}（可选 smoke/unit/integration/all）")

    REPORTS.mkdir(exist_ok=True)
    hits = REPORTS / "coverage_hits.json"
    if hits.exists():
        hits.unlink()  # 覆盖率按本次运行统计
    steps_dir = REPORTS / "steps"
    if steps_dir.exists():
        shutil.rmtree(steps_dir)  # 用例步骤捕获按本次运行统计

    # Allure 结果目录：全量套件共享（首层清理一次），跨层叠加出完整趋势
    allure_dir = None
    if not args.no_allure:
        try:
            import allure  # noqa: F401
            allure_dir = REPORTS / "allure-results"
            if allure_dir.exists():
                shutil.rmtree(allure_dir)
        except ImportError:
            print("提示：未安装 allure-pytest（pip install allure-pytest），跳过 Allure 结果生成")

    results = []
    failed = 0
    for name in names:
        print(f"\n===== {name} =====")
        junit = run_suite(name, fast=args.fast, allure_dir=allure_dir)
        r = parse_junit(junit)
        results.append(r)
        failed += r["counts"]["failed"] + r["counts"]["error"]
        print(f"{name}: {r['counts']['passed']} 通过 / {r['counts']['failed']} 失败 / "
              f"{r['counts']['error']} 错误 / {r['counts']['skipped']} 跳过（{r['time']}s）")

    total = dict(passed=0, failed=0, error=0, skipped=0)
    for r in results:
        for k in total:
            total[k] += r["counts"][k]
    html_path, md_path = build_report(results)

    # 干系人级详细报告（用例 ID/目标/前置/分步数据/预期/实际/状态/失败日志）
    from mw import detailed_report, endpoints
    cov = endpoints.summary(hits)
    ts = datetime.datetime.now().strftime("%Y%m%d_%H%M%S")
    detailed_report.build(results, steps_dir, cov,
                          REPORTS / f"detailed_report_{ts}.html",
                          REPORTS / "latest_detailed.md")
    print(f"详细报告：{REPORTS / 'latest_detailed.md'}")

    # Allure 后处理：用 docstring 回写用例名（夹具内 dynamic.title 不生效），
    # 随后探测 Allure CLI 渲染静态报告
    if allure_dir is not None:
        rename_from_docstring(allure_dir)
        n = len(glob.glob(str(allure_dir / "*result.json")))
        print(f"Allure 结果：{allure_dir}（{n} 用例）")
        allure_bin = shutil.which("allure")
        if allure_bin:
            out = REPORTS / "allure-report"
            subprocess.run([allure_bin, "generate", str(allure_dir),
                            "-o", str(out), "--clean"], capture_output=True)
            if (out / "index.html").exists():
                print(f"Allure 报告：{out / 'index.html'}（allure open {out} 查看）")
        else:
            print("提示：安装 Allure CLI（需 Java）后执行 "
                  "`allure serve tests/python/reports/allure-results` 查看交互式报告")

    print(f"\n报告：{html_path}\n摘要：{md_path}")
    notify(md_path, total)
    sys.exit(1 if failed else 0)


def rename_from_docstring(allure_dir: Path) -> None:
    """把 allure 结果的 name 回写为 docstring 首行（中文标题）。"""
    for f in glob.glob(str(Path(allure_dir) / "*result.json")):
        with open(f, encoding="utf-8") as fh:
            d = json.load(fh)
        desc = str(d.get("description") or d.get("descriptionHtml") or "")
        first = desc.strip().splitlines()[0].strip() if desc.strip() else ""
        if first and d.get("name") != first:
            d["name"] = f"{first}（{d.get('name', '')}）"
            with open(f, "w", encoding="utf-8") as fh:
                json.dump(d, fh, ensure_ascii=False)


if __name__ == "__main__":
    main()
