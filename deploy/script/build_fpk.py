#!/usr/bin/env python3
"""一键构建飞牛 FPK 安装包（native 形态）—— build.sh 的 Python 版本。

为什么要有 Python 版：
  build.sh 依赖 `dirname` / `node` / `npm` / `sed`，在 PATH 损坏的 Git Bash 里
  会随机失败（`dirname: command not found`、npm 报 `'bash': No such file or directory`）。
  本脚本只依赖 Python 标准库 + go，行为与 build.sh 等价。

流程：前端构建 → Go 静态编译 → 落 app/server → 版本号回写 manifest → 校验骨架 → fnpack 打包

用法：
  python deploy/script/build_fpk.py [版本号] [--skip-frontend] [--goos linux] [--goarch amd64]
                                    [--platform x86|arm] [--no-check]

多架构：
  包内含原生二进制，飞牛清单的 platform 必须与二进制架构一致（**不可写 all**）。
  --platform 缺省按 --goarch 推导（amd64→x86、arm64→arm）；推导表里没有的架构
  必须显式指定，否则直接报错退出（宁可失败也不要出一个架构标错、装到设备上起不来的包）。
"""

from __future__ import annotations

import argparse
import os
import pathlib
import re
import shutil
import subprocess
import sys

ROOT = pathlib.Path(__file__).resolve().parents[2]
BACKEND = ROOT / "backend"
FRONTEND = ROOT / "frontend"
FPK = ROOT / "deploy" / "fpk" / "metalwatch"
OUT_BIN = FPK / "app" / "server" / "metalwatch"

# goarch → manifest.platform 的映射。飞牛清单只认 x86 / arm，**不接受 all**：
# 包内含原生二进制，标成 all 会让 ARM 设备装到 x86 包然后启动失败。
# 见 deploy/tools/check_fpk.py 第 6 组断言与 docs/04-部署/01 第 5 节。
PLATFORM_OF_GOARCH = {"amd64": "x86", "arm64": "arm"}


def run(cmd: list[str], cwd: pathlib.Path | None = None, env: dict[str, str] | None = None,
        quiet: bool = False) -> int:
    """执行命令并实时回显；返回退出码。"""
    print(f"    $ {' '.join(cmd)}")
    p = subprocess.run(cmd, cwd=str(cwd) if cwd else None, env=env,
                       capture_output=True, text=True, encoding="utf-8", errors="replace")
    out = (p.stdout or "") + (p.stderr or "")
    if not quiet and out.strip():
        for line in out.splitlines()[-25:]:
            print("      |", line)
    return p.returncode


def npm_cmd() -> list[str] | None:
    """返回可用的 npm 调用方式。

    优先系统 npm（Linux/macOS/健康 Windows）；
    Windows 上 npm 是 shell 包装器，在损坏的 bash 里会失败，因此退化为
    node.exe 直接执行 npm-cli.js。
    """
    npm = shutil.which("npm") or shutil.which("npm.cmd")
    if npm and os.name != "nt":
        return [npm]
    candidates: list[pathlib.Path] = []
    if os.name == "nt":
        for base in (pathlib.Path.home() / ".workbuddy" / "binaries" / "node" / "versions",
                     pathlib.Path(r"D:\developmentEnv\nvm\.nodejs"),
                     pathlib.Path(r"C:\Program Files\nodejs")):
            if base.is_dir():
                candidates.extend(sorted(base.glob("*/node.exe"), reverse=True))
                candidates.append(base / "node.exe")
    for node in candidates:
        if not node.exists():
            continue
        cli = node.parent / "node_modules" / "npm" / "bin" / "npm-cli.js"
        if cli.exists():
            return [str(node), str(cli)]
    return [npm] if npm else None


def build_frontend() -> bool:
    print("==> [1/5] 构建前端")
    cmd = npm_cmd()
    if not cmd:
        print("    未找到 npm/node，跳过前端构建（请确保 frontend/dist 已是最新）")
        return True
    if not (FRONTEND / "node_modules").is_dir():
        if run(cmd + ["ci"], cwd=FRONTEND) != 0:
            return False
    return run(cmd + ["run", "build"], cwd=FRONTEND) == 0


def build_backend(version: str, goos: str, goarch: str) -> bool:
    print(f"==> [2/5] 编译服务端（CGO_ENABLED=0 静态二进制，{goos}/{goarch}）")
    OUT_BIN.parent.mkdir(parents=True, exist_ok=True)
    env = dict(os.environ)
    env.update(GOPROXY=os.environ.get("GOPROXY", "https://goproxy.cn,direct"),
               GOSUMDB=os.environ.get("GOSUMDB", "off"),
               CGO_ENABLED="0", GOOS=goos, GOARCH=goarch)
    out = OUT_BIN.with_suffix(".exe") if goos == "windows" else OUT_BIN
    rc = run(["go", "build", "-trimpath", "-ldflags", f"-s -w -X main.version={version}",
              "-o", str(out), "./cmd/server"], cwd=BACKEND, env=env)
    if rc != 0:
        return False
    if os.name != "nt":
        out.chmod(0o755)
    print(f"    产物 {out.stat().st_size} 字节")
    return True


def resolve_platform(goarch: str, explicit: str) -> str:
    """确定 manifest.platform 取值：显式参数优先，否则按 goarch 推导。

    推导不出来时**直接报错退出**，不要回落到 x86 —— 那会产出一个架构标错的包，
    且 check_fpk.py 只看「有没有二进制」，查不出这种错配。
    """
    if explicit:
        return explicit
    try:
        return PLATFORM_OF_GOARCH[goarch]
    except KeyError:
        raise SystemExit(
            f"无法从 --goarch={goarch} 推导 manifest.platform，请显式传 --platform x86|arm"
            f"（当前映射表覆盖：{sorted(PLATFORM_OF_GOARCH)}）") from None


def sync_version(version: str, platform: str) -> None:
    """把版本号与目标架构写进 manifest。

    必须在 check_fpk 之前调用 —— 校验脚本会核对 platform 与 app/server 下原生
    二进制的一致性，先写后验才有意义。
    """
    print(f"==> [3/5] 同步 manifest（version={version} platform={platform}）")
    mf = FPK / "manifest"
    text = mf.read_text(encoding="utf-8", newline="")
    new = re.sub(r"^version=.*$", f"version={version}", text, count=1, flags=re.M)
    new = re.sub(r"^platform=.*$", f"platform={platform}", new, count=1, flags=re.M)
    if new != text:
        mf.write_text(new, encoding="utf-8", newline="")


def check() -> bool:
    print("==> [4/5] 校验 FPK 骨架")
    return run([sys.executable, str(ROOT / "deploy" / "tools" / "check_fpk.py")], quiet=False) == 0


def pack(require: bool = False) -> bool:
    print("==> [5/5] fnpack 打包")
    # 先删旧产物：fnpack 失败时不会覆盖它，留着就会被当成「本轮产物」上报
    # （与 tests/python/run_tests.py 里 JUnit 旧文件同类的假绿来源）。
    stale = sorted(FPK.glob("*.fpk"))
    for f in stale:
        f.unlink()
    if stale:
        print(f"    已清理 {len(stale)} 个旧 .fpk")
    fnpack = shutil.which("fnpack")
    if not fnpack:
        print("    未找到 fnpack。安装方式见 https://developer.fnnas.com/docs/cli/fnpack/")
        if require:
            print("    --require-fnpack 已开启：缺 fnpack 直接判定失败（发布链路必须真出 .fpk）")
            return False
        print(f"    骨架已就绪：{FPK}（可手动执行 fnpack build）")
        return True
    rc = run([fnpack, "build"], cwd=FPK)
    produced = sorted(FPK.glob("*.fpk"))
    for f in produced:
        print(f"    产物 {f.name} {f.stat().st_size} 字节")
    if rc == 0 and not produced:
        print("    fnpack 退出码为 0 却没有任何 .fpk 产出 —— 判定失败")
        return False
    return rc == 0


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("version", nargs="?", default="")
    ap.add_argument("--skip-frontend", action="store_true")
    ap.add_argument("--goos", default="linux")
    ap.add_argument("--goarch", default="amd64")
    ap.add_argument("--platform", default="",
                    help="manifest.platform（x86/arm）。缺省按 --goarch 推导，推导不出则报错")
    ap.add_argument("--no-check", action="store_true")
    ap.add_argument("--require-fnpack", action="store_true",
                    help="缺 fnpack 时判定失败（CI 用；本地开发可不开）")
    args = ap.parse_args()

    version = args.version
    if not version:
        m = re.search(r"^version=(.+)$", (FPK / "manifest").read_text(encoding="utf-8"), re.M)
        version = m.group(1).strip() if m else "0.1.0"

    platform = resolve_platform(args.goarch, args.platform)

    if not args.skip_frontend and not build_frontend():
        print("前端构建失败")
        return 1
    if not build_backend(version, args.goos, args.goarch):
        print("服务端编译失败")
        return 1
    sync_version(version, platform)
    if not args.no_check and not check():
        print("骨架校验失败")
        return 1
    if not pack(args.require_fnpack):
        return 1
    print("\n构建完成：", FPK)
    for f in sorted(FPK.glob("*.fpk")):
        print("  ", f)
    return 0


if __name__ == "__main__":
    sys.exit(main())
