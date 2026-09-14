#!/bin/sh
# 生成安卓绑定库 caotun.aar(gomobile bind,上游官方 Go 即可,无需 fork)。
# 前置:
#   1. Android SDK(含 NDK,Android Studio → SDK Manager → NDK (Side by side))
#   2. 环境变量 ANDROID_HOME 指向 SDK;NDK 装在 $ANDROID_HOME/ndk/<版本>
#   3. go install golang.org/x/mobile/cmd/gomobile@latest
#      go install golang.org/x/mobile/cmd/gobind@latest
#      gomobile init
# 产物: mobile/android/app/libs/caotun.aar(Kotlin 侧 mobile.Mobile.* 直接调用)
set -e
ROOT="$(cd "$(dirname "$0")/../.." && pwd)"

if ! command -v gomobile >/dev/null 2>&1; then
  echo "错误: 未找到 gomobile,先安装:"
  echo "  go install golang.org/x/mobile/cmd/gomobile@latest"
  echo "  go install golang.org/x/mobile/cmd/gobind@latest"
  echo "  gomobile init"
  exit 1
fi

OUT_DIR="$ROOT/mobile/android/app/libs"
mkdir -p "$OUT_DIR"

cd "$ROOT/src"
# 纯 Go 绑定(无 cgo):引擎在安卓用上游 Go,不存在鸿蒙的 IE-TLS 问题
GOOS=android GOARCH=arm64 CGO_ENABLED=0 \
  gomobile bind -target=android -androidapi 29 \
  -ldflags="-s -w" \
  -o "$OUT_DIR/caotun.aar" ./mobile/android

echo "产物: $OUT_DIR/caotun.aar"
ls -lh "$OUT_DIR/caotun.aar"
