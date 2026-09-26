#!/usr/bin/env python3
"""MetalWatch 服务端运行时冒烟（W12 的本地部分）。

真机验证之前，先用与 deploy/fpk/metalwatch/cmd/main **完全相同**的参数形态拉起真实进程，
验证「能起来、能鉴权、能落盘、能恢复」这条最小闭环：

  配置加载 → 迁移 → 引导管理员 → 401 拦截 → 登录 → 带令牌访问 → 审计 → 重启数据恢复

不依赖 WSL / Docker / 真机：默认按当前平台编译（GOOS=linux 时需在 Linux 或 WSL 上运行）。

用法：
  python deploy/tools/smoke_server.py [--port 18099] [--goos windows] [--goarch amd64]
  python deploy/tools/smoke_server.py --binary path/to/metalwatch   # 冒烟现成产物（发布链路用）
"""

from __future__ import annotations

import argparse
import datetime
import json
import os
import pathlib
import platform
import re
import shutil
import subprocess
import sys
import tempfile
import time
import urllib.error
import urllib.request

ROOT = pathlib.Path(__file__).resolve().parents[2]
BACKEND = ROOT / "backend"

state: dict[str, object] = {}


def build(work: pathlib.Path, goos: str, goarch: str, version: str) -> pathlib.Path | None:
    print("==> [1/10] 编译服务端")
    exe = work / ("metalwatch.exe" if goos == "windows" else "metalwatch")
    env = dict(os.environ)
    env.update(GOPROXY=os.environ.get("GOPROXY", "https://goproxy.cn,direct"),
               GOSUMDB=os.environ.get("GOSUMDB", "off"),
               CGO_ENABLED="0", GOOS=goos, GOARCH=goarch)
    p = subprocess.run(["go", "build", "-trimpath", "-ldflags", f"-s -w -X main.version={version}",
                        "-o", str(exe), "./cmd/server"],
                       cwd=str(BACKEND), env=env,
                       capture_output=True, text=True, encoding="utf-8", errors="replace")
    if p.returncode != 0:
        print("    编译失败 rc=", p.returncode)
        print(((p.stdout or "") + (p.stderr or ""))[:3000])
        return None
    print(f"    产物 {exe.stat().st_size} 字节")
    return exe


def use_existing(path_str: str, work: pathlib.Path) -> pathlib.Path | None:
    """用现成二进制代替现场编译。

    存在的意义：发布流水线要验证的是**将要交付的那份文件本身**，而不是
    "拿同一份源码再编译一次的结果"——两者未必等价。本项目就踩过这类坑：
    `-trimpath` 抹掉 GOROOT 后二进制要靠内置 tzdata 才能解析时区，
    单测全绿、重新编译的冒烟也全绿，只有拿真产物跑才暴露启动即死。

    从 CI artifact 取回的文件没有可执行位，这里补上。
    """
    src = pathlib.Path(path_str).resolve()
    if not src.is_file():
        print(f"    指定二进制不存在：{src}")
        return None
    dst = work / src.name
    shutil.copyfile(src, dst)
    if os.name != "nt":
        dst.chmod(0o755)
    print(f"    使用现成二进制 {src.name}（{dst.stat().st_size} 字节）")
    return dst


def http(base: str, method: str, path: str, token: str | None = None, body: dict | None = None):
    data = json.dumps(body).encode() if body is not None else None
    req = urllib.request.Request(base + path, data=data, method=method)
    req.add_header("Content-Type", "application/json")
    if token:
        req.add_header("Authorization", "Bearer " + token)
    try:
        with urllib.request.urlopen(req, timeout=10) as r:
            raw = r.read().decode("utf-8") or "{}"
            try:
                return r.status, json.loads(raw)
            except Exception:
                return r.status, {"raw": raw[:200]}
    except urllib.error.HTTPError as e:
        raw = e.read().decode("utf-8", "replace")
        try:
            return e.code, json.loads(raw or "{}")
        except Exception:
            return e.code, {"raw": raw[:200]}


def wait_ready(base: str, proc: subprocess.Popen, tries: int = 40) -> bool:
    for _ in range(tries):
        time.sleep(0.5)
        if proc.poll() is not None:
            return False
        try:
            st, _ = http(base, "GET", "/healthz")
            if st == 200:
                return True
        except Exception:
            pass
    return False


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--port", type=int, default=18099)
    ap.add_argument("--goos", default="windows" if os.name == "nt" else "linux")
    ap.add_argument("--goarch", default="amd64")
    ap.add_argument("--version", default="0.1.0-smoke")
    ap.add_argument("--binary", default="",
                    help="跳过编译，直接冒烟这个现成二进制（发布链路用它验证待交付的产物本身）")
    ap.add_argument("--keep", action="store_true", help="保留本次运行目录便于排查")
    args = ap.parse_args()

    base = f"http://127.0.0.1:{args.port}"
    work = pathlib.Path(tempfile.mkdtemp(prefix="mw-smoke-"))
    print(f"运行目录：{work}")
    data = work / "data"
    logs = work / "logs"
    data.mkdir()
    logs.mkdir()
    pid_file = work / "metalwatch.pid"

    exe = use_existing(args.binary, work) if args.binary \
        else build(work, args.goos, args.goarch, args.version)
    if not exe:
        return 1

    ok = True

    def check(label: str, cond: bool, extra: str = "") -> None:
        nonlocal ok
        ok = ok and cond
        print(f"    [{'ok' if cond else 'FAIL'}] {label}{(' — ' + extra) if extra else ''}")

    argv = [str(exe), "--data", str(data), "--listen", f"127.0.0.1:{args.port}",
            "--log-dir", str(logs), "--pid-file", str(pid_file)]

    print("==> [2/10] 拉起进程（FPK 同款参数）")
    out1 = (logs / "stdout.log").open("w", encoding="utf-8")
    proc = subprocess.Popen(argv, stdout=out1, stderr=subprocess.STDOUT)
    try:
        ready = wait_ready(base, proc)
        if not ready:
            print(logs.joinpath("stdout.log").read_text(encoding="utf-8", errors="replace")[:3000])
        check("就绪探测 /healthz", ready)
        if not ready:
            return 1

        print("==> [3/10] PID 文件")
        pid_txt = pid_file.read_text(encoding="utf-8").strip() if pid_file.exists() else ""
        check("PID 文件写入", pid_txt.isdigit(), pid_txt)

        print("==> [4/10] 未认证访问应被拦截")
        st, body = http(base, "GET", "/api/v1/hosts")
        check("未带令牌 GET /api/v1/hosts → 401", st == 401, f"{st} {body.get('code','')}")

        print("==> [5/10] 首次启动引导管理员")
        boot = data / "bootstrap_admin.txt"
        check("引导文件生成", boot.exists())
        if not boot.exists():
            return 1
        text = boot.read_text(encoding="utf-8", errors="replace")
        m = re.search(r"^\s*password\s*[:=]\s*(\S+)\s*$", text, re.M)
        password = m.group(1) if m else ""
        un = re.search(r"^\s*username\s*[:=]\s*(\S+)\s*$", text, re.M)
        username = un.group(1) if un else "admin"
        check("口令可解析", bool(password), f"{len(password)} 字符")

        print("==> [6/10] 登录")
        st, body = http(base, "POST", "/api/v1/auth/login",
                        body={"username": username, "password": password})
        d = body.get("data") or body
        token = d.get("token", "") if st == 200 else ""
        check("登录成功并签发令牌", st == 200 and bool(token), f"{st} len={len(token)}")
        if not token:
            print("    响应:", json.dumps(body, ensure_ascii=False)[:300])
            return 1

        st, _ = http(base, "POST", "/api/v1/auth/login",
                     body={"username": username, "password": "wrong-pass"})
        check("错误口令被拒 → 401", st == 401, str(st))

        print("==> [7/10] 带令牌访问")
        st, _ = http(base, "GET", "/api/v1/hosts", token=token)
        check("GET /api/v1/hosts", st == 200, str(st))
        st, body = http(base, "GET", "/api/v1/auth/me", token=token)
        me = body.get("data") or body
        check("GET /auth/me 身份正确", st == 200 and me.get("role") == "admin",
              f"{me.get('username')}/{me.get('role')}")

        print("==> [8/10] 审计落库")
        st, body = http(base, "GET", "/api/v1/audit-logs", token=token)
        d = body.get("data") or body
        items = d.get("items") or d.get("list") or []
        check("审计有记录", st == 200 and len(items) > 0, f"{len(items)} 条")

        print("==> [9/10] 数据落盘")
        files = sorted(p.name for p in data.rglob("*") if p.is_file())
        need = {"metalwatch.db", "master.key", "tsdb.db"}
        check("关键数据文件齐备", need.issubset(set(files)), ", ".join(files))

        print("==> [10/10] 重启复用（对应真机重装/升级数据恢复）")
        proc.terminate()
        try:
            proc.wait(timeout=15)
        except subprocess.TimeoutExpired:
            proc.kill()
        out1.close()
        if pid_file.exists():
            pid_file.unlink()

        out2 = (logs / "stdout2.log").open("w", encoding="utf-8")
        proc = subprocess.Popen(argv, stdout=out2, stderr=subprocess.STDOUT)
        ready2 = wait_ready(base, proc)
        check("重启后就绪", ready2)
        st, body = http(base, "GET", "/api/v1/auth/me", token=token)
        me2 = body.get("data") or body
        check("旧令牌重启后仍有效", st == 200, f"{st} {me2.get('username','')}")
        check("引导管理员未重复生成",
              boot.read_text(encoding="utf-8", errors="replace") == text)
    finally:
        try:
            proc.terminate()
            proc.wait(timeout=15)
        except Exception:
            try:
                proc.kill()
            except Exception:
                pass
        try:
            out1.close()
        except Exception:
            pass
        print("==> 进程已停止")

    if args.keep:
        print(f"保留运行目录：{work}")
    else:
        shutil.rmtree(work, ignore_errors=True)

    print()
    print("结果：", "全部通过" if ok else "存在失败项")
    return 0 if ok else 1


if __name__ == "__main__":
    sys.exit(main())
