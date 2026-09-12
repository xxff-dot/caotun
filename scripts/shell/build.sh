#!/bin/sh
# caotun 打包脚本（Linux / macOS；Windows 用 windows/build.bat）
# 编译三平台 + UPX 压缩(自动下载) + 收集启停脚本，产物输出到 dist/
# 用法: sh scripts/shell/build.sh
cd "$(dirname "$0")/../../src" || exit 1
mkdir -p ../dist

command -v go >/dev/null 2>&1 || { echo "错误: 未找到 go 命令"; exit 1; }

echo "[1/3] 编译（去符号表）..."
rm -f ../dist/caotun.exe ../dist/caotun_linux ../dist/caotun_macos
go build -ldflags "-s -w" -o ../dist/caotun.exe . || exit 1
GOOS=linux GOARCH=amd64 go build -ldflags "-s -w" -o ../dist/caotun_linux . || exit 1
GOOS=darwin GOARCH=arm64 go build -ldflags "-s -w" -o ../dist/caotun_macos . || echo "darwin 编译失败(不影响其他平台)"

# ---- [2/3] UPX：自检 → 没有则按本机系统自动下载（GitHub 直连失败自动走镜像；macOS 跳过）----
UPX_VER=5.0.2
MIRROR="${UPX_MIRROR:-https://gh-proxy.com/}"   # GitHub 镜像前缀，可用环境变量 UPX_MIRROR 覆盖
fetch() { # fetch URL OUT
  command -v curl >/dev/null 2>&1 && { curl -fsSL --connect-timeout 15 -o "$2" "$1" && return 0; }
  command -v wget >/dev/null 2>&1 && { wget -q -T 15 -O "$2" "$1" && return 0; }
  return 1
}
fetch_any() { # fetch_any OUT URL1 [URL2 ...] 依次尝试
  OUT="$1"; shift
  for u in "$@"; do
    echo "  下载: $u"
    if fetch "$u" "$OUT"; then return 0; fi
    echo "  失败，换下一个源"
    rm -f "$OUT"
  done
  return 1
}
ensure_upx() {
  command -v upx >/dev/null 2>&1 && { UPX_BIN="upx"; return 0; }
  case "$(uname -s)" in
    MINGW*|MSYS*|CYGWIN*) KIND=win ;;
    *) KIND=linux ;;
  esac
  UDIR="$ROOTDIR/tools/upx-$KIND"
  if [ "$KIND" = win ]; then
    [ -f "$UDIR/upx-$UPX_VER-win64/upx.exe" ] && { UPX_BIN="$UDIR/upx-$UPX_VER-win64/upx.exe"; return 0; }
  else
    [ -x "$UDIR/upx" ] && { UPX_BIN="$UDIR/upx"; return 0; }
  fi
  GH="https://github.com/upx/upx/releases/download/v$UPX_VER"
  mkdir -p "$UDIR"
  if [ "$KIND" = win ]; then
    echo "自动下载 upx (Windows)..."
    fetch_any "$UDIR/upx.zip" "$GH/upx-$UPX_VER-win64.zip" "$MIRROR$GH/upx-$UPX_VER-win64.zip" || return 1
    if command -v unzip >/dev/null 2>&1; then
      unzip -q -o "$UDIR/upx.zip" -d "$UDIR"
    else
      powershell -NoProfile -Command "Expand-Archive -Force '$(cygpath -w "$UDIR/upx.zip")' '$(cygpath -w "$UDIR")'"
    fi
    UPX_BIN="$UDIR/upx-$UPX_VER-win64/upx.exe"
  else
    echo "自动下载 upx (Linux)..."
    fetch_any "$UDIR/upx.tar.xz" "$GH/upx-$UPX_VER-amd64_linux.tar.xz" "$MIRROR$GH/upx-$UPX_VER-amd64_linux.tar.xz" || return 1
    tar -xJf "$UDIR/upx.tar.xz" -C "$UDIR" --strip-components=1 || return 1
    UPX_BIN="$UDIR/upx"
  fi
  "$UPX_BIN" --version >/dev/null 2>&1 && return 0
  echo "  下载的 upx 无法执行"
  return 1
}

case "$(uname -s)" in
  Darwin)
    echo "[2/3] macOS 不适用 UPX，跳过压缩"
    ;;
  *)
    ROOTDIR="$(cd ../.. && pwd)"
    UPX_BIN=""
    if ensure_upx; then
      echo "[2/3] UPX 压缩..."
      # 分文件压缩：单个失败不影响另一个
      "$UPX_BIN" -q -f ../dist/caotun.exe || echo "caotun.exe 保留未压缩版本"
      "$UPX_BIN" -q -f ../dist/caotun_linux || echo "caotun_linux 保留未压缩版本"
    else
      echo "[2/3] UPX 不可用且自动下载失败，跳过压缩。"
      echo "      可手动放置 upx 后重跑（Windows: tools/upx-win/upx-$UPX_VER-win64/upx.exe；Linux: tools/upx-linux/upx）"
    fi
    ;;
esac

# ---- [3/3] 启动/停止脚本复制进 dist/（shell/ 与 windows/ 分目录），开箱即用 ----
echo "[3/3] 收集启停脚本到 dist/ ..."
mkdir -p ../dist/shell ../dist/windows
rm -f ../dist/*.sh ../dist/*.bat ../dist/*.conf
cp -f ../scripts/shell/*.sh ../dist/shell/ 2>/dev/null
cp -f ../scripts/windows/*.bat ../dist/windows/ 2>/dev/null
cp -f ../scripts/client.conf ../scripts/server.conf ../dist/ 2>/dev/null
rm -f ../dist/*.~

# ---- [4/4] 组装 Release 包（供 GitHub Release 挂载 / install-server.sh 远程下载）----
echo "[4/4] 组装 Release 包..."
tar -czf ../dist/caotun-linux.tar.gz -C ../dist caotun_linux shell server.conf client.conf 2>/dev/null
if command -v zip >/dev/null 2>&1; then
  (cd .. && zip -q -r dist/caotun-windows.zip dist/caotun.exe dist/windows dist/client.conf dist/server.conf)
elif command -v powershell >/dev/null 2>&1; then
  (cd .. && powershell -NoProfile -Command "Compress-Archive -Force -Path 'dist\caotun.exe','dist\windows','dist\client.conf','dist\server.conf' -DestinationPath 'dist\caotun-windows.zip'")
else
  echo "跳过 windows zip（缺 zip 命令，可手动打包）"
fi

ls -la ../dist/ | awk '{print $5, $9}'
