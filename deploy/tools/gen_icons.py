"""生成 MetalWatch 应用图标（纯标准库，无第三方依赖）。

输出：
  deploy/fpk/metalwatch/ICON.PNG           64x64
  deploy/fpk/metalwatch/ICON_256.PNG       256x256
  deploy/fpk/metalwatch/app/ui/images/icon_64.png
  deploy/fpk/metalwatch/app/ui/images/icon_256.png

用法：python deploy/tools/gen_icons.py
"""

from __future__ import annotations

import math
import os
import struct
import zlib

NAVY = (15, 23, 42)
PANEL = (30, 41, 59)
CYAN = (56, 189, 248)
AMBER = (251, 191, 36)
RED = (248, 113, 113)

SS = 4  # 超采样倍数，用于抗锯齿


def _rounded_rect(px: float, py: float, x0: float, y0: float, x1: float, y1: float,
                  r: float) -> float:
    """返回点 (px,py) 到圆角矩形的覆盖度（0/1）。"""
    if not (x0 <= px <= x1 and y0 <= py <= y1):
        return 0.0
    cx = min(max(px, x0 + r), x1 - r)
    cy = min(max(py, y0 + r), y1 - r)
    return 1.0 if math.hypot(px - cx, py - cy) <= r else 0.0


def _circle(px: float, py: float, cx: float, cy: float, r: float) -> float:
    return 1.0 if math.hypot(px - cx, py - cy) <= r else 0.0


def sample(u: float, v: float) -> tuple[int, int, int, int]:
    """按归一化坐标返回该采样点的 RGBA。"""
    # 外底板
    if not _rounded_rect(u, v, 0.03, 0.03, 0.97, 0.97, 0.20):
        return (0, 0, 0, 0)

    color = NAVY
    alpha = 255

    # 三个机架槽位
    bars = [
        (0.20, 0.66, 0.24, 0.35, CYAN),
        (0.20, 0.66, 0.445, 0.555, CYAN),
        (0.20, 0.52, 0.65, 0.76, AMBER),
    ]
    for bx0, bx1, by0, by1, fill in bars:
        if _rounded_rect(u, v, bx0, by0, bx1, by1, 0.045):
            color = fill
            # 槽位内部再压一条深色"散热缝"，增加辨识度
            if by0 + 0.035 < v < by1 - 0.035 and bx0 + 0.06 < u < bx1 - 0.06:
                color = PANEL

    # 右下故障指示灯
    if _circle(u, v, 0.705, 0.705, 0.062):
        color = RED

    return (color[0], color[1], color[2], alpha)


def render(size: int) -> list[list[tuple[int, int, int, int]]]:
    rows = []
    inv = 1.0 / (size * SS)
    for y in range(size):
        row = []
        for x in range(size):
            acc_r = acc_g = acc_b = acc_a = 0
            for sy in range(SS):
                for sx in range(SS):
                    u = (x * SS + sx + 0.5) * inv
                    v = (y * SS + sy + 0.5) * inv
                    r, g, b, a = sample(u, v)
                    acc_r += r * a
                    acc_g += g * a
                    acc_b += b * a
                    acc_a += a
            n = SS * SS
            if acc_a == 0:
                row.append((0, 0, 0, 0))
            else:
                row.append((round(acc_r / acc_a), round(acc_g / acc_a),
                            round(acc_b / acc_a), round(acc_a / n)))
        rows.append(row)
    return rows


def write_png(path: str, rows: list[list[tuple[int, int, int, int]]]) -> None:
    height = len(rows)
    width = len(rows[0])
    raw = bytearray()
    for row in rows:
        raw.append(0)  # filter type 0
        for r, g, b, a in row:
            raw += bytes((r, g, b, a))

    def chunk(tag: bytes, data: bytes) -> bytes:
        return (struct.pack(">I", len(data)) + tag + data
                + struct.pack(">I", zlib.crc32(tag + data) & 0xFFFFFFFF))

    ihdr = struct.pack(">IIBBBBB", width, height, 8, 6, 0, 0, 0)
    blob = (b"\x89PNG\r\n\x1a\n"
            + chunk(b"IHDR", ihdr)
            + chunk(b"IDAT", zlib.compress(bytes(raw), 9))
            + chunk(b"IEND", b""))
    os.makedirs(os.path.dirname(path), exist_ok=True)
    with open(path, "wb") as fh:
        fh.write(blob)
    print(f"written {path} ({len(blob)} bytes)")


def main() -> None:
    root = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
    pkg = os.path.join(root, "fpk", "metalwatch")
    small = render(64)
    large = render(256)
    targets = [
        (os.path.join(pkg, "ICON.PNG"), small),
        (os.path.join(pkg, "ICON_256.PNG"), large),
        (os.path.join(pkg, "app", "ui", "images", "icon_64.png"), small),
        (os.path.join(pkg, "app", "ui", "images", "icon_256.png"), large),
    ]
    for path, rows in targets:
        write_png(path, rows)


if __name__ == "__main__":
    main()
