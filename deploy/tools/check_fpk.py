"""校验 FPK 工程骨架（Native 形态）。

检查项：
  1. 打包强校验文件是否齐全（manifest / config/privilege / config/resource / ICON）
  2. JSON 文件合法性
  3. cmd 脚本为 LF 换行且带 shebang（CRLF 会导致真机执行失败）
  4. 端口一致性：manifest.service_port == app/ui/config 入口 port
  5. Native 形态断言：不得声明 docker-project；cmd/main 必须是 PID 文件托管进程
  6. platform 与原生二进制的一致性（有原生二进制时不得为 all）
  7. 向导字段与 cmd 脚本 / 配置渲染的引用一致性
"""

from __future__ import annotations

import json
import pathlib
import re
import sys

ROOT = pathlib.Path(__file__).resolve().parents[1]
PKG = ROOT / "fpk" / "metalwatch"

problems: list[str] = []
warnings: list[str] = []


def ok(msg: str) -> None:
    print(f"  [ok]   {msg}")


def bad(msg: str) -> None:
    problems.append(msg)
    print(f"  [FAIL] {msg}")


def warn(msg: str) -> None:
    warnings.append(msg)
    print(f"  [warn] {msg}")


print("=== 1. 打包强校验文件 ===")
for rel in ("manifest", "config/privilege", "config/resource", "ICON.PNG", "ICON_256.PNG"):
    (ok if (PKG / rel).exists() else bad)(f"{rel} 存在")

print("\n=== 2. JSON 合法性 ===")
json_files = ["config/privilege", "config/resource", "app/ui/config",
              "wizard/install", "wizard/config", "wizard/uninstall"]
parsed: dict[str, object] = {}
for rel in json_files:
    fp = PKG / rel
    if not fp.exists():
        bad(f"{rel} 缺失")
        continue
    try:
        parsed[rel] = json.loads(fp.read_text(encoding="utf-8"))
        ok(f"{rel} JSON 合法")
    except Exception as exc:  # noqa: BLE001
        bad(f"{rel} JSON 非法: {exc}")

print("\n=== 3. cmd 脚本换行与 shebang ===")
for p in sorted((PKG / "cmd").iterdir()):
    blob = p.read_bytes()
    crlf = blob.count(b"\r\n")
    if crlf:
        bad(f"cmd/{p.name} 含 {crlf} 处 CRLF（真机将执行失败）")
    elif blob[:2] != b"#!":
        bad(f"cmd/{p.name} 缺少 shebang")
    else:
        ok(f"cmd/{p.name} LF + shebang")

print("\n=== 4. 端口一致性 ===")
manifest_text = (PKG / "manifest").read_text(encoding="utf-8")
m = re.search(r"^service_port=(\d+)\s*$", manifest_text, re.M)
ui = parsed.get("app/ui/config", {})
ui_entry = (ui or {}).get(".url", {}).get("metalwatch.main", {}) if isinstance(ui, dict) else {}
if not m:
    bad("manifest 缺少 service_port")
else:
    m_port = m.group(1)
    ui_port = str(ui_entry.get("port", ""))
    if m_port == ui_port:
        ok(f"service_port={m_port} 与桌面入口 port 一致")
    else:
        bad(f"manifest.service_port={m_port} 与 app/ui/config port={ui_port} 不一致")

print("\n=== 5. Native 形态断言 ===")
res = parsed.get("config/resource", {})
if isinstance(res, dict):
    if "docker-project" in res:
        bad("config/resource 仍声明 docker-project（本项目为非容器实现）")
    else:
        ok("未声明 docker-project")
    if "data-share" in res:
        shares = [s.get("name") for s in res["data-share"].get("shares", [])]
        ok(f"data-share: {shares}")
    else:
        warn("未声明 data-share，报表/备份将无处落盘")

main_script = (PKG / "cmd" / "main").read_text(encoding="utf-8")
for token, desc in (("PID_FILE", "PID 文件托管"), ("kill -TERM", "TERM 优雅停止"),
                    ("kill -KILL", "KILL 兜底"), ("exit 3", "未运行返回 3")):
    (ok if token in main_script else bad)(f"cmd/main 含 {desc}（{token}）")
if "docker" in main_script:
    bad("cmd/main 仍引用 docker（本项目为非容器实现）")

print("\n=== 6. platform 与原生二进制一致性 ===")
plat = re.search(r"^platform=(\w+)\s*$", manifest_text, re.M)
has_binary = any((PKG / "app" / "server").glob("*")) if (PKG / "app" / "server").is_dir() else False
plat_val = plat.group(1) if plat else ""
if plat_val == "all" and has_binary:
    bad("app/server 下有原生产物，但 manifest.platform=all（应改为 x86 或 arm）")
else:
    ok(f"platform={plat_val}（含原生二进制时应为 x86/arm）")
if plat_val == "x86":
    warn("仅覆盖 x86：ARM 设备需另出 platform=arm 的包")

print("\n=== 7. 向导字段引用一致性 ===")
fields: set[str] = set()
for name in ("install", "config", "uninstall"):
    steps = parsed.get(f"wizard/{name}", [])
    if isinstance(steps, list):
        for step in steps:
            for item in step.get("items", []):
                if "field" in item:
                    fields.add(item["field"])

referenced: set[str] = set()
for p in (PKG / "cmd").iterdir():
    referenced |= set(re.findall(r"\$\{(wizard_[a-z_]+)", p.read_text(encoding="utf-8")))

undefined = sorted(referenced - fields)
if undefined:
    bad(f"cmd 脚本引用了未定义的向导字段: {undefined}")
else:
    ok(f"cmd 引用的 {len(referenced)} 个向导字段均已定义")
unused = sorted(fields - referenced)
if unused:
    warn(f"向导定义了但 cmd 未使用（可能仅由应用自身读取）: {unused}")

print("\n" + "=" * 56)
if problems:
    print(f"结果：{len(problems)} 项失败，{len(warnings)} 项警告")
    for p in problems:
        print(f"  - {p}")
    sys.exit(1)
print(f"结果：全部通过（{len(warnings)} 项警告）")
