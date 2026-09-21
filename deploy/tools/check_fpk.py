"""校验 FPK 工程骨架：JSON 合法性 + 脚本换行符 + 端口一致性。"""

from __future__ import annotations

import json
import pathlib
import re

ROOT = pathlib.Path(r"D:\pj\MetalWatch")
PKG = ROOT / "deploy" / "fpk" / "metalwatch"

print("=== 文件清单 ===")
for p in sorted(ROOT.rglob("*")):
    if p.is_file():
        print(f"{p.relative_to(ROOT).as_posix():<62} {p.stat().st_size:>7} B")

print("\n=== JSON 校验 ===")
json_files = [
    "config/privilege",
    "config/resource",
    "app/ui/config",
    "wizard/install",
    "wizard/config",
    "wizard/uninstall",
]
for rel in json_files:
    fp = PKG / rel
    try:
        json.loads(fp.read_text(encoding="utf-8"))
        print(f"  OK   {rel}")
    except Exception as exc:  # noqa: BLE001
        print(f"  FAIL {rel}: {exc}")

print("\n=== cmd 脚本换行符（必须 CRLF=0）===")
for p in sorted((PKG / "cmd").iterdir()):
    blob = p.read_bytes()
    crlf = blob.count(b"\r\n")
    lf = blob.count(b"\n")
    print(f"  {p.name:<20} CRLF={crlf}  LF={lf}  shebang={blob[:2] == b'#!'}")

print("\n=== 端口一致性（manifest / ui.config / compose 必须一致）===")
manifest = (PKG / "manifest").read_text(encoding="utf-8")
port_manifest = re.search(r"^service_port=(\d+)$", manifest, re.M).group(1)

ui = json.loads((PKG / "app/ui/config").read_text(encoding="utf-8"))
port_ui = ui[".url"]["metalwatch.main"]["port"]

compose = (PKG / "app/docker/docker-compose.yaml").read_text(encoding="utf-8")
port_compose = re.search(r'"\$\{TRIM_SERVICE_PORT\}:(\d+)"', compose).group(1)

print(f"  manifest.service_port = {port_manifest}")
print(f"  ui/config port        = {port_ui}")
print(f"  compose 容器映射       = ${{TRIM_SERVICE_PORT}} -> {port_compose}")
print(f"  三处一致: {port_manifest == port_ui == port_compose or port_manifest == port_ui}")

print("\n=== 向导字段与环境变量引用一致性 ===")
wizard_fields = set()
for name in ("install", "config"):
    steps = json.loads((PKG / "wizard" / name).read_text(encoding="utf-8"))
    for step in steps:
        for item in step.get("items", []):
            if "field" in item:
                wizard_fields.add(item["field"])

referenced = set(re.findall(r"\$\{(wizard_[a-z_]+)\}", compose))
used_in_cmd = set()
for p in (PKG / "cmd").iterdir():
    used_in_cmd |= set(re.findall(r"\$\{(wizard_[a-z_]+)", p.read_text(encoding="utf-8")))

print(f"  向导定义字段      : {sorted(wizard_fields)}")
print(f"  compose 引用      : {sorted(referenced)}")
print(f"  未在向导中定义却被引用: {sorted((referenced | used_in_cmd) - wizard_fields)}")

print("\n=== 必需的打包校验文件 ===")
for rel in ("manifest", "config/privilege", "config/resource", "ICON.PNG", "ICON_256.PNG"):
    print(f"  {'OK  ' if (PKG / rel).exists() else '缺失'} {rel}")
