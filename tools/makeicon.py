#!/usr/bin/env python3
"""图标构建：从 icon.svg 正确渲染多尺寸 PNG，并打包为符合规范的 .ico。

注意：渲染时**不能**用 --window-size=<小尺寸> ——那会把 256x256 的 SVG 裁掉，
只取左上角。正确做法是按 1:1 渲染 256，再用 LANCZOS 缩放。
"""
import io
import os
import struct
import subprocess
import sys

from PIL import Image

SIZES = [16, 24, 32, 48, 64, 128, 256]


def find_browser() -> str:
    """按优先级查找一个可用的 Chromium 内核浏览器（Chrome / Edge / Playwright 版）。"""
    for key in ("KTC_CHROME", "CHROME_PATH"):
        p = os.environ.get(key)
        if p and os.path.exists(p):
            return p

    home = os.path.expanduser("~")
    local = os.environ.get("LOCALAPPDATA") or os.path.join(home, "AppData", "Local")
    drive = os.environ.get("SystemDrive", "C:")

    candidates = []
    pw = os.path.join(local, "ms-playwright")
    if os.path.isdir(pw):
        for name in sorted(os.listdir(pw), reverse=True):
            for sub in ("chrome-win64", "chrome-win", "chrome-win32"):
                candidates.append(os.path.join(pw, name, sub, "chrome.exe"))
    candidates.append(os.path.join(local, "Google", "Chrome", "Application", "chrome.exe"))

    progdirs = [
        os.environ.get("PROGRAMFILES"),
        os.environ.get("PROGRAMFILES(X86)"),
        os.environ.get("PROGRAMW6432"),
        drive + r"\Program Files",
        drive + r"\Program Files (x86)",
    ]
    for pf in progdirs:
        if not pf:
            continue
        candidates.append(os.path.join(pf, "Google", "Chrome", "Application", "chrome.exe"))
        candidates.append(os.path.join(pf, "Microsoft", "Edge", "Application", "msedge.exe"))

    for p in candidates:
        if p and os.path.exists(p):
            return p
    raise SystemExit(
        "未找到 Chromium 内核浏览器（Chrome / Edge）。\n"
        "请安装其中之一，或设置环境变量 KTC_CHROME 指向 chrome.exe / msedge.exe。"
    )


def render_master(root: str) -> Image.Image:
    """把 SVG 以 256x256 渲染为 RGBA 主图。"""
    svg = io.open(os.path.join(root, "assets", "icon.svg"), encoding="utf-8").read()
    html = ('<!doctype html><meta charset="utf-8">'
            '<style>html,body{margin:0;background:transparent;overflow:hidden}svg{display:block}</style>' + svg)
    tmp_html = os.path.join(root, ".build", "icon-master.html")
    tmp_png = os.path.join(root, ".build", "icon-master.png")
    os.makedirs(os.path.dirname(tmp_html), exist_ok=True)
    io.open(tmp_html, "w", encoding="utf-8").write(html)
    url = "file:///" + tmp_html.replace("\\", "/").replace(" ", "%20")
    subprocess.run([find_browser(), "--headless=new", "--disable-gpu", "--no-sandbox",
                    "--hide-scrollbars", "--default-background-color=00000000",
                    "--force-device-scale-factor=1", "--window-size=256,256",
                    f"--screenshot={tmp_png}", url],
                   check=True, capture_output=True)
    return Image.open(tmp_png).convert("RGBA")


def dib_entry(img: Image.Image) -> bytes:
    """BITMAPINFOHEADER + BGRA 像素（自下而上）+ AND 掩码。"""
    im = img.convert("RGBA")
    w, h = im.size
    px = im.load()
    rows = []
    for y in range(h - 1, -1, -1):
        row = bytearray()
        for x in range(w):
            r, g, b, a = px[x, y]
            row += bytes((b, g, r, a))
        rows.append(bytes(row))
    xormask = b"".join(rows)
    and_row = ((w + 31) // 32) * 4
    andmask = b"\x00" * (and_row * h)
    hdr = struct.pack("<IiiHHIIiiII", 40, w, h * 2, 1, 32, 0,
                      len(xormask) + len(andmask), 0, 0, 0, 0)
    return hdr + xormask + andmask


def png_entry(img: Image.Image) -> bytes:
    buf = io.BytesIO()
    img.convert("RGBA").save(buf, format="PNG", optimize=True)
    return buf.getvalue()


def main(root: str) -> None:
    root = os.path.abspath(root)
    master = render_master(root)
    out_dir = os.path.join(root, ".build", "icon")
    os.makedirs(out_dir, exist_ok=True)

    entries = []
    for s in SIZES:
        im = master if s == 256 else master.resize((s, s), Image.LANCZOS)
        im.save(os.path.join(out_dir, f"icon-{s}.png"))
        entries.append((s, png_entry(im) if s >= 256 else dib_entry(im)))

    ico = os.path.join(root, "assets", "KillTheC.ico")
    header = struct.pack("<HHH", 0, 1, len(entries))
    offset = len(header) + 16 * len(entries)
    dirs, blob = b"", b""
    for s, data in entries:
        dim = s if s < 256 else 0
        dirs += struct.pack("<BBBBHHII", dim, dim, 0, 0, 1, 32, len(data), offset)
        blob += data
        offset += len(data)
    with open(ico, "wb") as f:
        f.write(header + dirs + blob)

    # 供 go-winres 使用的主图
    master.save(os.path.join(root, "assets", "icon-256.png"))
    print("已生成:", ico, os.path.getsize(ico), "字节；尺寸:", [s for s, _ in entries])

    # 自检：中心不透明、四角透明
    # 注意取「四角顶点」而非内缩点——小尺寸下圆角只剩 1px 宽，内缩点会落在渐变区误报。
    bad = []
    for s in SIZES:
        im = Image.open(ico)
        im.size = (s, s)
        im.load()
        rgba = im.convert("RGBA")
        center = rgba.getpixel((s // 2, s // 2))[3]
        corners = [rgba.getpixel(p)[3] for p in ((0, 0), (s - 1, 0), (0, s - 1), (s - 1, s - 1))]
        ok = center > 200 and max(corners) < 128
        if not ok:
            bad.append(s)
        print(f"  {s:>3}x{s:<3} 中心alpha={center:>3} 四角alpha={str(corners):<24} {'OK' if ok else '!! 异常'}")
    if bad:
        raise SystemExit(f"图标自检未通过：{bad}")


if __name__ == "__main__":
    default_root = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
    main(sys.argv[1] if len(sys.argv) > 1 else default_root)
