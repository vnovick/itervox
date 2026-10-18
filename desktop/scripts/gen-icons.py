#!/usr/bin/env python3
"""Generates the app and tray icons in desktop/src-tauri/icons/.

Pure standard library (zlib + struct), so it runs anywhere:
    python3 desktop/scripts/gen-icons.py

The mark is the itervox "I//" glyph: a teal bar and two violet slashes on the
dashboard's dark tile (site/public/favicon.svg colours). The tray icon is a
template image (black + alpha) that macOS tints for light and dark menu bars.
"""

import os
import struct
import sys
import zlib

OUT = os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", "src-tauri", "icons")

BG = (0x11, 0x11, 0x13)
TEAL = (0x14, 0xB8, 0xA6)
VIOLET_A = (0x96, 0x78, 0xE3)
VIOLET_B = (0xB8, 0xA5, 0xEB)


def png(width, height, rgba_rows):
    raw = b"".join(b"\x00" + bytes(row) for row in rgba_rows)

    def chunk(tag, data):
        c = struct.pack(">I", len(data)) + tag + data
        return c + struct.pack(">I", zlib.crc32(tag + data) & 0xFFFFFFFF)

    return (
        b"\x89PNG\r\n\x1a\n"
        + chunk(b"IHDR", struct.pack(">IIBBBBB", width, height, 8, 6, 0, 0, 0))
        + chunk(b"IDAT", zlib.compress(raw, 9))
        + chunk(b"IEND", b"")
    )


def in_round_rect(x, y, x0, y0, x1, y1, r):
    if x < x0 or x > x1 or y < y0 or y > y1:
        return False
    cx = min(max(x, x0 + r), x1 - r)
    cy = min(max(y, y0 + r), y1 - r)
    return (x - cx) ** 2 + (y - cy) ** 2 <= r * r


def in_slash(x, y, bx, top, bottom, lean, width):
    # Parallelogram: left edge runs from (bx, bottom) to (bx + lean, top).
    if y < top or y > bottom:
        return False
    t = (bottom - y) / (bottom - top)
    left = bx + lean * t
    return left <= x <= left + width


def glyph(x, y, ox=0.0, oy=0.0, s=1.0):
    """Which part of the glyph covers (x, y); None outside. Coordinates of
    the glyph are in a unit box scaled by s and offset by (ox, oy)."""
    u, v = (x - ox) / s, (y - oy) / s
    if 0.10 <= u <= 0.24 and 0.12 <= v <= 0.88:
        return "bar"
    for bx in (0.34, 0.60):
        if in_slash(u, v, bx, 0.12, 0.88, 0.26, 0.14):
            return "slash"
    return None


def render_app(size, ss=3):
    rows = []
    margin, radius = 0.09, 0.2
    for py in range(size):
        row = []
        for px in range(size):
            acc = [0.0, 0.0, 0.0, 0.0]
            for sy in range(ss):
                for sx in range(ss):
                    x = (px + (sx + 0.5) / ss) / size
                    y = (py + (sy + 0.5) / ss) / size
                    if not in_round_rect(x, y, margin, margin, 1 - margin, 1 - margin, radius):
                        continue
                    part = glyph(x, y, ox=0.24, oy=0.26, s=0.5)
                    if part == "bar":
                        c = TEAL
                    elif part == "slash":
                        t = min(max((x - 0.4) / 0.4, 0.0), 1.0)
                        c = tuple(round(a + (b - a) * t) for a, b in zip(VIOLET_A, VIOLET_B))
                    else:
                        c = BG
                    acc[0] += c[0]
                    acc[1] += c[1]
                    acc[2] += c[2]
                    acc[3] += 255
            n = ss * ss
            a = acc[3] / n
            if a > 0:
                cov = acc[3] / 255
                row += [round(acc[0] / cov), round(acc[1] / cov), round(acc[2] / cov), round(a)]
            else:
                row += [0, 0, 0, 0]
        rows.append(row)
    return rows


def render_tray(size, ss=4):
    rows = []
    for py in range(size):
        row = []
        for px in range(size):
            hits = 0
            for sy in range(ss):
                for sx in range(ss):
                    x = (px + (sx + 0.5) / ss) / size
                    y = (py + (sy + 0.5) / ss) / size
                    if glyph(x, y, ox=0.06, oy=0.06, s=0.88):
                        hits += 1
            row += [0, 0, 0, round(255 * hits / (ss * ss))]
        rows.append(row)
    return rows


def downsample(rows, size, factor):
    out = []
    n = size // factor
    for y in range(n):
        row = []
        for x in range(n):
            acc = [0, 0, 0, 0]
            for dy in range(factor):
                src = rows[y * factor + dy]
                for dx in range(factor):
                    i = (x * factor + dx) * 4
                    a = src[i + 3]
                    acc[0] += src[i] * a
                    acc[1] += src[i + 1] * a
                    acc[2] += src[i + 2] * a
                    acc[3] += a
            if acc[3]:
                row += [
                    round(acc[0] / acc[3]),
                    round(acc[1] / acc[3]),
                    round(acc[2] / acc[3]),
                    round(acc[3] / (factor * factor)),
                ]
            else:
                row += [0, 0, 0, 0]
        out.append(row)
    return out


def icns(entries):
    body = b"".join(tag + struct.pack(">I", len(data) + 8) + data for tag, data in entries)
    return b"icns" + struct.pack(">I", len(body) + 8) + body


def main():
    os.makedirs(OUT, exist_ok=True)
    big = render_app(1024, ss=2)
    pngs = {1024: png(1024, 1024, big)}
    for s in (512, 256, 128, 64, 32, 16):
        pngs[s] = png(s, s, downsample(big, 1024, 1024 // s))
    files = {
        "32x32.png": pngs[32],
        "128x128.png": pngs[128],
        "128x128@2x.png": pngs[256],
        "icon.png": pngs[512],
        "icon.icns": icns(
            [
                (b"icp4", pngs[16]),
                (b"icp5", pngs[32]),
                (b"ic07", pngs[128]),
                (b"ic08", pngs[256]),
                (b"ic09", pngs[512]),
                (b"ic10", pngs[1024]),
                (b"ic11", pngs[32]),
                (b"ic12", pngs[64]),
                (b"ic13", pngs[256]),
                (b"ic14", pngs[512]),
            ]
        ),
        "tray.png": png(44, 44, render_tray(44)),
    }
    for name, data in files.items():
        with open(os.path.join(OUT, name), "wb") as f:
            f.write(data)
        print(f"wrote {name} ({len(data)} bytes)")
    return 0


if __name__ == "__main__":
    sys.exit(main())
