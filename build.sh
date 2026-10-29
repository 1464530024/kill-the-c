#!/usr/bin/env bash
# Kill the C —— 一键构建脚本
# 产出：dist/KillTheC-Setup.exe（安装包）
# 依赖：Go 工具链（首次运行会从 goproxy.cn 自动下载到 .tools/go）
set -euo pipefail

ROOT="$(cd "$(dirname "$0")" && pwd)"
BUILD="$ROOT/.build"
DIST="$ROOT/dist"
GO_VER="go1.25.14"

# ── 基础环境 ─────────────────────────────────────────────────────────────
# 如果 python / curl 不在 PATH 中，可通过 KTC_EXTRA_PATH 追加自定义目录
if [ -n "${KTC_EXTRA_PATH:-}" ]; then
  export PATH="$KTC_EXTRA_PATH:$PATH"
fi
for tool in python curl; do
  if ! command -v "$tool" >/dev/null 2>&1; then
    echo "错误：未找到 $tool。请安装后加入 PATH，或设置 KTC_EXTRA_PATH 指向其所在目录。" >&2
    exit 1
  fi
done

export GOROOT="$ROOT/.tools/go"
export GOPATH="$BUILD/gopath"
export GOMODCACHE="$BUILD/gomodcache"
export GOCACHE="$BUILD/gocache"
export GOPROXY="https://goproxy.cn,direct"
export GOSUMDB="off"
export GOTOOLCHAIN="local"
export GOFLAGS="-mod=mod"
export CGO_ENABLED=0

# ── 缺少 Go 工具链时自动安装 ─────────────────────────────────────────────
if [ ! -x "$GOROOT/bin/go.exe" ]; then
  echo "[1/6] 下载 Go 工具链 $GO_VER …"
  mkdir -p "$BUILD" "$ROOT/.tools"
  ZIP="$BUILD/go-toolchain.zip"
  curl -sL --retry 3 -o "$ZIP" \
    "https://goproxy.cn/golang.org/toolchain/@v/v0.0.1-${GO_VER}.windows-amd64.zip"
  python - "$ZIP" "$ROOT/.tools" <<'PY'
import os, sys, zipfile
zip_path, dst = sys.argv[1], sys.argv[2]
prefix = "golang.org/toolchain@v0.0.1-go1.25.14.windows-amd64/"
with zipfile.ZipFile(zip_path) as z:
    for info in z.infolist():
        if not info.filename.startswith(prefix):
            continue
        rel = info.filename[len(prefix):]
        if not rel:
            continue
        target = os.path.join(dst, "go", rel.replace("/", os.sep))
        if info.is_dir():
            os.makedirs(target, exist_ok=True)
            continue
        os.makedirs(os.path.dirname(target), exist_ok=True)
        with z.open(info) as src, open(target, "wb") as out:
            while True:
                chunk = src.read(1 << 20)
                if not chunk:
                    break
                out.write(chunk)
print("Go 工具链已解压")
PY
fi
export PATH="$ROOT/.tools/go/bin:$PATH"

# ── 资源生成工具 ─────────────────────────────────────────────────────────
if [ ! -x "$GOPATH/bin/go-winres.exe" ]; then
  echo "[2/6] 安装 go-winres …"
  go install github.com/tc-hib/go-winres@latest
fi
export PATH="$GOPATH/bin:$PATH"

mkdir -p "$DIST"

# ── 主程序 ───────────────────────────────────────────────────────────────
echo "[3/6] 检查图标资源 …"
if [ ! -f "$ROOT/assets/icon-256.png" ]; then
  python "$ROOT/tools/makeicon.py" "$ROOT"
fi
echo "[3/6] 构建主程序 KillTheC.exe …"
cd "$ROOT/src"
rm -f rsrc_windows_amd64.syso
go-winres simply --arch amd64 --out rsrc --manifest gui \
  --icon "$ROOT/assets/icon-256.png" \
  --product-name "Kill the C" \
  --file-description "Kill the C 磁盘清理专家" \
  --product-version "1.0.0.0" --file-version "1.0.0.0" \
  --copyright "Copyright (C) 2026" \
  --original-filename "KillTheC.exe" >/dev/null
go build -trimpath -ldflags "-s -w -H=windowsgui" -o "$BUILD/KillTheC.exe" .

# ── 压缩负载 ─────────────────────────────────────────────────────────────
echo "[4/6] 压缩应用负载 …"
python - "$BUILD/KillTheC.exe" "$ROOT/installer/payload/app.gz" <<'PY'
import gzip, shutil, sys, os
src, dst = sys.argv[1], sys.argv[2]
os.makedirs(os.path.dirname(dst), exist_ok=True)
with open(src, "rb") as f, gzip.open(dst, "wb", compresslevel=9) as g:
    shutil.copyfileobj(f, g, 1 << 20)
print("app.exe %d B -> app.gz %d B" % (os.path.getsize(src), os.path.getsize(dst)))
PY

# ── 安装程序 ─────────────────────────────────────────────────────────────
echo "[5/6] 构建安装程序 KillTheC-Setup.exe …"
cd "$ROOT/installer"
rm -f rsrc_windows_amd64.syso
go-winres simply --arch amd64 --out rsrc --manifest gui \
  --icon "$ROOT/assets/icon-256.png" \
  --product-name "Kill the C" \
  --file-description "Kill the C 安装程序" \
  --product-version "1.0.0.0" --file-version "1.0.0.0" \
  --copyright "Copyright (C) 2026" \
  --original-filename "KillTheC-Setup.exe" >/dev/null
go build -trimpath -ldflags "-s -w -H=windowsgui" -o "$DIST/KillTheC-Setup.exe" .

# ── 汇总 ─────────────────────────────────────────────────────────────────
echo "[6/6] 完成"
SIZE=$(stat -c%s "$DIST/KillTheC-Setup.exe")
echo "  安装包：$DIST/KillTheC-Setup.exe"
echo "  体积  ：$((SIZE/1024/1024)).$(( (SIZE/1024%1024)*10/1024 )) MB ($SIZE 字节)"
if [ "$SIZE" -gt 20971520 ]; then
  echo "  ⚠️  超出 20MB 限制！"
  exit 1
fi
echo "  ✅ 符合 20MB 以内要求"
