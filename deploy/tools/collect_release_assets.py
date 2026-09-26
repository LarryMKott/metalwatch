#!/usr/bin/env python3
"""把各平台构建产物归集、改名、算校验和，供发布使用。

为什么要有这个脚本（而不是在 workflow 里写内联 shell）：
  发布链路的「产物是否齐全、命名是否规范」是最容易出错也最难回头补救的一环
  —— 少传一个 Agent 包、x86 与 arm 的 fpk 互相覆盖，都会在用户下载后才发现。
  把规则写成一个可本地运行的脚本，就能在推 tag 之前用假目录先验证一遍，
  而不是等 CI 跑完再肉眼检查。

输入目录布局（对应 actions/download-artifact 不带 merge-multiple 的落盘结构）：
    <raw>/<artifact 名>/<文件...>

用法：
    python deploy/tools/collect_release_assets.py --raw raw --out dist --version 1.2.3

出口约定：
  产物齐全、命名无歧义 → 0，并写出 SHA256SUMS
  缺产物 / 多产物 / 命名冲突 → 1（宁可失败，也不要发布一个不完整的版本）
"""

from __future__ import annotations

import argparse
import hashlib
import pathlib
import shutil
import sys

# artifact 名 → 规范文件名。artifact 名必须与 release.yml 里 actions/upload-artifact
# 的 name 完全一致；改了 workflow 就要同步改这里，否则会在「缺产物」处报错。
BINARY_KINDS = ("server", "agent")
TARGET_PLATFORMS = ("linux-amd64", "linux-arm64", "windows-amd64")

# FPK 按架构出包：飞牛清单的 platform 只有 x86 / arm 两个合法值
# （见 docs/04-部署/01 第 5 节，写 all 会被 check_fpk.py 断言拦下）。
FPK_ARTIFACTS = {"fpk-x86": "x86_64", "fpk-arm": "arm64"}

WEBUI_ARTIFACT = "webui-dist"


def canonical_map(version: str) -> dict[str, str]:
    """返回 {artifact 名: 规范文件名}。"""
    out: dict[str, str] = {}
    for kind in BINARY_KINDS:
        for plat in TARGET_PLATFORMS:
            ext = ".exe" if plat.startswith("windows") else ""
            out[f"{kind}-{plat}"] = f"metalwatch-{kind}-{plat}{ext}"
    for artifact, arch in FPK_ARTIFACTS.items():
        out[artifact] = f"metalwatch-fpk-{arch}-{version}.fpk"
    out[WEBUI_ARTIFACT] = f"metalwatch-webui-{version}.tar.gz"
    return out


def sha256_of(path: pathlib.Path) -> str:
    h = hashlib.sha256()
    with open(path, "rb") as fh:
        for chunk in iter(lambda: fh.read(1 << 20), b""):
            h.update(chunk)
    return h.hexdigest()


def collect(raw: pathlib.Path, out: pathlib.Path, version: str) -> int:
    expected = canonical_map(version)
    out.mkdir(parents=True, exist_ok=True)

    problems: list[str] = []
    produced: list[tuple[str, pathlib.Path, int]] = []

    for artifact, target_name in expected.items():
        src_dir = raw / artifact
        if not src_dir.is_dir():
            problems.append(f"缺少产物目录：{src_dir.relative_to(raw.parent)}（artifact={artifact}）")
            continue

        files = sorted(p for p in src_dir.rglob("*") if p.is_file())
        if not files:
            problems.append(f"{artifact}: 目录为空，没有可发布的文件")
            continue
        if len(files) > 1:
            # 多文件一定意味着上传规则写宽了（例如把整个 bin/ 上传了）。
            # 这种情况下「哪个才是要发布的」无法由脚本判断，直接报错让人去改 workflow。
            names = ", ".join(f.name for f in files[:5])
            problems.append(f"{artifact}: 期望 1 个文件，实得 {len(files)} 个（{names}）")
            continue

        src = files[0]
        dst = out / target_name
        shutil.copyfile(src, dst)
        produced.append((artifact, dst, dst.stat().st_size))

    if problems:
        print("归集失败：")
        for p in problems:
            print(f"  ✗ {p}")
        return 1

    # SHA256SUMS 用与 sha256sum 完全一致的格式（"<hash>  <文件名>"），
    # 便于用户在设备上用 `sha256sum -c SHA256SUMS` 直接校验。
    sums = out / "SHA256SUMS"
    lines = []
    for _, path, _ in sorted(produced, key=lambda t: t[1].name):
        lines.append(f"{sha256_of(path)}  {path.name}")
    sums.write_text("\n".join(lines) + "\n", encoding="utf-8", newline="\n")

    width = max(len(n) for n in (p.name for _, p, _ in produced)) if produced else 0
    print(f"版本 {version}，共 {len(produced)} 项产物：")
    for _, path, size in sorted(produced, key=lambda t: t[1].name):
        print(f"  {path.name:<{width}}  {size / 1048576:8.2f} MB")
    print(f"  {'SHA256SUMS':<{width}}  {sums.stat().st_size} B")
    return 0


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--raw", required=True, help="download-artifact 落盘目录")
    ap.add_argument("--out", required=True, help="归集输出目录")
    ap.add_argument("--version", required=True, help="版本号（不带 v 前缀）")
    args = ap.parse_args()

    raw = pathlib.Path(args.raw)
    if not raw.is_dir():
        print(f"输入目录不存在：{raw}")
        return 1
    return collect(raw, pathlib.Path(args.out), args.version)


if __name__ == "__main__":
    sys.exit(main())
