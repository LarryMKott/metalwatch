#!/usr/bin/env python3
"""从 Git 提交历史自动生成 Release 发布日志（本次修改记录）。

两份产物分工：
  - -o <file>          只含本次版本的发布说明，作 Release **描述**（CI 在发布前生成，
                       经 `gh release create --notes-file` 提交，发布页即「本次改了什么」）
  - --update-changelog 把同一份内容累积进 CHANGELOG.md（仓库内的变更历史正文）

机制与分组规则移植自 fn-finstat 项目的 scripts/gen_release_notes.py（见其
docs/发布流程与Release日志.md），按 MetalWatch 的差异做了裁剪：

  - MetalWatch 没有 VERSION 文件，版本号由 release.yml 的 meta job 传入（--tag 必填）
  - 没有 dev 渠道与渠道别名产物，安装/校验指引按 MetalWatch 的资产矩阵写
  - Gitee 的「描述必须取自入库文件」约束在 GitHub（gh --notes-file）不存在，
    故 RELEASE_NOTES.md 由 CI 构建期生成而非入库（已进 .gitignore），
    CHANGELOG.md 仍入库作为累积历史

与上游一致的硬约束（改代码前先读）：
  - 仅依赖 Python 标准库，CI（3.12）与本机（3.13）都能跑；写文件显式 LF
  - 遵循约定式提交，无法识别的提交归入「其他变更」，绝不丢提交
  - 破坏性变更只认标题上的 ! 标记（正文 BREAKING CHANGE 读不到，也不做子串匹配）
  - 兼容浅克隆：定位不到基线时自动退化为最近 N 个提交，日志生成不阻断发布
  - 幂等：同一版本重复生成会覆盖 CHANGELOG 中已有的同名段落，且不截断

基线（本次日志的起点）推断优先级：
  1. 命令行 --from <ref>
  2. CHANGELOG.md 顶部段落里的 <!-- release-baseline: <sha> --> 标记
  3. 最近的语义化版本 tag（形如 v1.2.3；构建号 tag v39 会被跳过）
  4. 最近 --limit 个提交

用法：
  python deploy/tools/gen_release_notes.py --tag 0.2.0                    # 打印到标准输出
  python deploy/tools/gen_release_notes.py --tag 0.3.0 -o RELEASE_NOTES.md  # CI 发布描述
  python deploy/tools/gen_release_notes.py --tag 0.3.0 --update-changelog   # 累积进 CHANGELOG
"""

from __future__ import annotations

import argparse
import os
import re
import subprocess
import sys
from datetime import datetime, timezone
from pathlib import Path

# ---------------------------------------------------------------- 分组定义

# 顺序即输出顺序：重要的放前面
GROUP_ORDER: list[tuple[str, str]] = [
    ("security", "🔒 安全修复"),
    ("feat", "✨ 新功能"),
    ("fix", "🐛 问题修复"),
    ("perf", "⚡ 性能优化"),
    ("refactor", "♻️ 代码重构"),
    ("test", "🧪 测试"),
    ("ci", "👷 构建与流水线"),
    ("build", "📦 打包构建"),
    ("docs", "📝 文档"),
    ("style", "🎨 样式调整"),
    ("chore", "🔧 杂项维护"),
    ("revert", "⏪ 版本回退"),
]
BREAKING_TITLE = "💥 破坏性变更"
OTHER_TITLE = "📌 其他变更"

# 常见 type 别名 → 标准分组
TYPE_ALIASES = {
    "feature": "feat",
    "features": "feat",
    "feat": "feat",
    "bugfix": "fix",
    "hotfix": "fix",
    "fixes": "fix",
    "fix": "fix",
    "sec": "security",
    "security": "security",
    "perf": "perf",
    "performance": "perf",
    "refactor": "refactor",
    "refactoring": "refactor",
    "test": "test",
    "tests": "test",
    "ci": "ci",
    "build": "build",
    "doc": "docs",
    "docs": "docs",
    "style": "style",
    "chore": "chore",
    "chores": "chore",
    "deps": "chore",
    "revert": "revert",
}

# 约定式提交：type(scope)!: subject   —— 同时兼容中文冒号
COMMIT_RE = re.compile(
    r"^(?P<type>[A-Za-z]+)"
    r"(?:\((?P<scope>[^)]*)\))?"
    r"(?P<breaking>!)?"
    r"\s*[:：]\s*"
    r"(?P<subject>.+)$"
)

BASELINE_RE = re.compile(r"<!--\s*release-baseline:\s*(?P<sha>[0-9a-fA-F]{6,40})\s*-->")
# 段落**自身**的起点。与 release-baseline（供"下次从哪开始"推断）是两件事：
# 后者随每次生成前移，若拿它当起点重生成，同一版本的段落会被"截断"成只剩
# 自上次生成以来的提交（fn-finstat 实测：0.7.3 段落从 13 个提交缩成 2 个）。
RELEASE_START_RE = re.compile(
    r"<!--\s*release-start:\s*(?P<sha>[0-9a-fA-F]{6,40})\s*-->"
)
# 正式发版 tag：形如 v1.2.3。刻意用 $ 锚定、不接受预发布后缀——
# 若带预发布后缀的 tag（如 v1.2.3-rc1）也算成发版基线，正式版日志的起点会
# 落在"最后一次非正式构建"上，中间的变更会从日志里凭空消失。
# 构建号 tag（v39）同样被排除在外。
SEMVER_TAG_RE = re.compile(r"^v\d+\.\d+\.\d+$")

SEP = "\x1f"  # git log 字段分隔符，避免与提交信息冲突


# ---------------------------------------------------------------- Git 封装


def run_git(*args: str) -> str | None:
    """执行 git 命令，失败时返回 None（脚本要能容忍 CI 环境的各种怪状态）。"""
    try:
        out = subprocess.run(
            ["git", *args],
            capture_output=True,
            text=True,
            check=False,
        )
    except FileNotFoundError:
        return None
    if out.returncode != 0:
        return None
    return out.stdout.strip()


def run_git_checked(*args: str) -> tuple[bool, str]:
    """执行 git 命令，返回 (是否成功, 输出)。

    与 run_git 的区别：能区分「命令成功但输出为空」和「命令失败」——
    前者是真实结果（基线之后没有新提交），不该触发兜底。
    """
    try:
        out = subprocess.run(
            ["git", *args],
            capture_output=True,
            text=True,
            check=False,
        )
    except FileNotFoundError:
        return False, ""
    return out.returncode == 0, out.stdout.strip()


def repo_root() -> Path:
    top = run_git("rev-parse", "--show-toplevel")
    return Path(top) if top else Path(__file__).resolve().parent.parent.parent


def is_shallow() -> bool:
    return run_git("rev-parse", "--is-shallow-repository") == "true"


def ref_exists(ref: str) -> bool:
    return run_git("rev-parse", "--verify", "--quiet", ref) is not None


def current_sha() -> str:
    return run_git("rev-parse", "HEAD") or ""


def write_text_lf(path: Path, text: str) -> None:
    """显式写 LF 换行

    `Path.write_text` 在 Windows 会按 `os.linesep` 写成 CRLF，而仓库里所有文本都是
    LF（.gitattributes 强制 eol=lf）。工作区留下一份 CRLF 只会让后续 diff、校验与
    「同一文件两份换行」的困惑反复出现。注意 `Path.read_text(newline=...)` 是
    Python 3.13 才有的参数，读侧统一用 open() —— 发布流水线是 3.12（D38 踩过）。
    """
    with open(path, "w", encoding="utf-8", newline="\n") as fp:
        fp.write(text)


def resolve_baseline(changelog: Path, limit: int) -> tuple[str, str]:
    """返回 (起点 ref, 推断方式说明)。"""
    # 2. CHANGELOG 基线标记
    if changelog.exists():
        match = BASELINE_RE.search(changelog.read_text(encoding="utf-8"))
        if match and ref_exists(match.group("sha")):
            return match.group("sha"), "CHANGELOG 基线标记"

    # 3. 最近的语义化版本 tag（跳过 v39 这类构建号 tag）
    tags = run_git("tag", "--list", "--sort=-creatordate")
    if tags:
        for tag in tags.splitlines():
            tag = tag.strip()
            if tag and SEMVER_TAG_RE.match(tag) and ref_exists(tag):
                return tag, f"语义化版本 tag {tag}"

    # 4. 兜底：最近 limit 个提交
    return f"HEAD~{limit}", f"最近 {limit} 个提交"


def collect_commits(start: str, end: str, limit: int) -> tuple[list[dict], str]:
    """采集提交。范围为空时自动放宽为最近 limit 个提交。"""
    fmt = f"--pretty=%H{SEP}%s{SEP}%an{SEP}%h"

    def parse(raw: str) -> list[dict]:
        items = []
        for line in raw.splitlines():
            if not line.strip():
                continue
            parts = line.split(SEP)
            if len(parts) < 4:
                continue
            items.append(
                {
                    "sha": parts[0],
                    "subject": parts[1],
                    "author": parts[2],
                    "short": parts[3],
                }
            )
        return items

    def log(*ref_args: str) -> tuple[list[dict], bool]:
        """返回 (提交列表, git 是否成功)。成功但为空是真实结果，不是失败。"""
        ok, raw = run_git_checked("log", "--no-merges", "--reverse", fmt, *ref_args)
        return parse(raw), ok

    # 起点是根提交（首个版本没有上一版基线时的典型取法）时，`root..end` 会把
    # root 本身排除在外——首个版本的日志恰恰不能少它。此时直接取全量历史。
    parents = run_git("rev-list", "--parents", "-n", "1", start)
    if parents and len(parents.split()) == 1:
        commits, _ = log(end)
        return commits, f"{start}（根提交）..{end}"

    commits, ok = log(f"{start}..{end}")
    if commits or ok:
        # 空 = 基线之后确实没有提交（例如重发同一提交），如实输出，不兜底
        return commits, f"{start}..{end}"

    # git 失败（浅克隆拿不到历史等）→ 退化为最近 N 个提交
    commits, _ = log("-n", str(limit), end)
    return commits, f"最近 {limit} 个提交"


# ---------------------------------------------------------------- 解析与渲染


def group_commits(commits: list[dict]) -> dict[str, list[dict]]:
    buckets: dict[str, list[dict]] = {key: [] for key, _ in GROUP_ORDER}
    buckets[BREAKING_TITLE] = []
    buckets[OTHER_TITLE] = []

    for commit in commits:
        match = COMMIT_RE.match(commit["subject"])
        if not match:
            buckets[OTHER_TITLE].append(commit)
            continue

        ctype = match.group("type").lower()
        scope = (match.group("scope") or "").strip()
        breaking = bool(match.group("breaking"))

        commit["scope"] = scope
        commit["clean_subject"] = match.group("subject").strip()
        # 破坏性变更单独成组，同时保留在原始分组中。
        # 只认标题上显式的 ! 标记：不能用 "BREAKING CHANGE" in subject 这类子串判断，
        # 否则 "docs: 补充 BREAKING CHANGE 章节说明" 会被误判为破坏性变更。
        if breaking:
            buckets[BREAKING_TITLE].append(commit)

        key = TYPE_ALIASES.get(ctype)
        (buckets[key] if key else buckets[OTHER_TITLE]).append(commit)

    return buckets


def render_entry(commit: dict) -> str:
    scope = commit.get("scope")
    subject = commit.get("clean_subject") or commit["subject"]
    prefix = f"**{scope}**: " if scope else ""
    return f"- {prefix}{subject} ({commit['short']})"


def render(version: str, commits: list[dict], range_desc: str, date_str: str = "") -> str:
    date = date_str or datetime.now(timezone.utc).strftime("%Y-%m-%d")
    buckets = group_commits(commits)

    authors = sorted({c["author"] for c in commits if c.get("author")})
    lines: list[str] = [f"## MetalWatch v{version}", ""]
    meta = [f"📅 发布日期：{date}", f"🔢 提交数量：{len(commits)}"]
    if authors:
        meta.append(f"👥 贡献者：{'、'.join(authors)}")
    lines.append("> " + " · ".join(meta))
    lines.append("")

    if not commits:
        lines.append("本次发布没有检测到新的提交记录。")
        lines.append("")
    else:
        for key, title in GROUP_ORDER:
            items = buckets.get(key) or []
            if not items:
                continue
            lines.append(f"### {title}")
            lines.append("")
            lines.extend(render_entry(c) for c in items)
            lines.append("")

        for title in (BREAKING_TITLE, OTHER_TITLE):
            items = buckets.get(title) or []
            if not items:
                continue
            lines.append(f"### {title}")
            lines.append("")
            lines.extend(render_entry(c) for c in items)
            lines.append("")

    lines.append("---")
    lines.append("")
    # 安装与校验指引必须写在描述正文里：附件列表里孤零零一个 SHA256SUMS，
    # 用户不一定知道它是干什么用的 —— 发布了校验文件却不告诉用户怎么用，等于没发。
    lines.append(
        "**安装（飞牛 fnOS）**：下载与应用架构一致的 FPK —— x86 设备用 "
        f"`metalwatch-fpk-x86_64-{version}.fpk`、ARM 设备用 "
        f"`metalwatch-fpk-arm64-{version}.fpk`，在应用中心手动安装。"
    )
    lines.append(
        "**安装（二进制）**：其他部署方式按平台下载 `metalwatch-server-*` 与 "
        "`metalwatch-agent-*`，WebUI 为 "
        f"`metalwatch-webui-{version}.tar.gz`（解压后由服务端托管）。"
    )
    lines.append(
        "**校验（SHA-256）**：下载附件 `SHA256SUMS`，与资产放在同一目录后执行 "
        "`sha256sum -c SHA256SUMS`（Windows 可用 "
        "`certutil -hashfile <文件> SHA256` 逐个对照）。"
    )
    lines.append(f"**变更范围**：{range_desc}")
    lines.append("")
    return "\n".join(lines)


# ---------------------------------------------------------------- CHANGELOG


def section_pattern(version: str) -> re.Pattern[str]:
    """匹配某版本的段落（从 `## ` 标题到下一个 `## ` 之前）

    两处必须收紧，否则会波及相邻版本的段落：

    1. 版本号后必须跟非数字、非点号的字符（或行尾）—— 否则 `v0.2.1` 会命中
       `v0.2.10`，生成 0.2.1 的日志时把 0.2.10 的段落整段替换掉。
    2. 标题部分用 `[^\\n]` 而不是 `.`：整个正则带 DOTALL，用 `.` 会让标题段
       跨行去后面找版本号，匹配起点被提前到**上一个版本**的标题上。
    """
    return re.compile(
        rf"^## [^\n]*?v{re.escape(version)}(?![0-9.])[^\n]*$.*?(?=^## |\Z)",
        flags=re.MULTILINE | re.DOTALL,
    )


def pinned_start(changelog: Path, version: str) -> str:
    """该版本段落已记录的起点 sha（没有则空串）

    同一版本被反复生成时必须沿用已记录的起点 —— 否则段落会越跑越短。
    """
    if not changelog.exists():
        return ""
    text = changelog.read_text(encoding="utf-8")
    match = section_pattern(version).search(text)
    if not match:
        return ""
    m = RELEASE_START_RE.search(match.group(0))
    if m and ref_exists(m.group("sha")):
        return m.group("sha")
    return ""


def update_changelog(changelog: Path, version: str, body: str, baseline: str, start: str) -> None:
    header = (
        "# 更新日志\n\n"
        "本文件由 `deploy/tools/gen_release_notes.py` 自动生成，请勿手工编辑已发布版本的内容。\n"
        "提交信息请遵循[约定式提交](https://www.conventionalcommits.org/zh-hans/)。\n"
    )
    section = body + f"\n<!-- release-baseline: {baseline} -->\n"
    if start:
        # 记下本段落的起点，供同版本重生成时沿用（见 pinned_start）
        section += f"<!-- release-start: {start} -->\n"

    if not changelog.exists():
        write_text_lf(changelog, f"{header}\n{section}")
        return

    text = changelog.read_text(encoding="utf-8")

    # 已存在同名版本段 → 替换（保证重复运行幂等）
    pattern = section_pattern(version)
    if pattern.search(text):
        text = pattern.sub(lambda _: section, text, count=1)
    else:
        # 否则插到第一个版本段之前
        first = re.search(r"^## ", text, flags=re.MULTILINE)
        if first:
            text = text[: first.start()] + section + "\n" + text[first.start() :]
        else:
            text = text.rstrip() + "\n\n" + section

    write_text_lf(changelog, text)


# ---------------------------------------------------------------- 入口


def main() -> int:
    parser = argparse.ArgumentParser(description="自动生成 Release 发布日志与 CHANGELOG")
    parser.add_argument("--tag", required=True, help="版本号（形如 0.2.0，不带 v 前缀；必填，MetalWatch 没有 VERSION 文件）")
    parser.add_argument("-o", "--output", help="输出文件路径（默认打印到标准输出；CI 传 RELEASE_NOTES.md 作发布描述）")
    parser.add_argument("--from", dest="from_ref", help="日志起点 ref（默认自动推断）")
    parser.add_argument("--to", dest="to_ref", default="HEAD", help="日志终点 ref，默认 HEAD")
    parser.add_argument("--date", help="发布日期 YYYY-MM-DD（默认今天）")
    parser.add_argument("--limit", type=int, default=30, help="无法推断基线时的兜底提交数，默认 30")
    parser.add_argument(
        "--update-changelog",
        action="store_true",
        help="把本次说明累积写入 CHANGELOG.md（同版本会覆盖，幂等）",
    )
    parser.add_argument("--changelog", default="CHANGELOG.md", help="CHANGELOG 路径")
    args = parser.parse_args()

    root = repo_root()
    os.chdir(root)

    if is_shallow():
        print("⚠️  检测到浅克隆，提交历史可能不完整", file=sys.stderr)

    version = args.tag.lstrip("v")
    changelog = root / args.changelog

    # 同版本重复生成时沿用该段落已记录的起点（--from 显式指定时以它为准）
    if args.from_ref:
        start, source = args.from_ref, "--from 指定"
        if not ref_exists(start):
            print(f"⚠️  起点 {start} 不存在，改用自动推断", file=sys.stderr)
            start, source = resolve_baseline(changelog, args.limit)
    else:
        pinned = pinned_start(changelog, version)
        if pinned:
            start, source = pinned, "该版本已记录的起点"
        else:
            start, source = resolve_baseline(changelog, args.limit)

    commits, range_desc = collect_commits(start, args.to_ref, args.limit)
    if not commits:
        print("⚠️  未采集到任何提交，Release 说明将只包含概要", file=sys.stderr)

    print(
        f"==> 版本 {version} · 基线 {start}（{source}）· {len(commits)} 个提交",
        file=sys.stderr,
    )

    body = render(version, commits, range_desc, date_str=args.date)

    if args.output:
        out = Path(args.output)
        write_text_lf(out, body)
        print(f"==> 已写入 {out}", file=sys.stderr)
    else:
        print(body)

    if args.update_changelog:
        update_changelog(changelog, version, body, current_sha() or args.to_ref, start=start)
        print(f"==> 已更新 {changelog}", file=sys.stderr)

    return 0


if __name__ == "__main__":
    sys.exit(main())
