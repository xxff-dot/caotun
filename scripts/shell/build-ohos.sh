#!/bin/sh
# 交叉编译鸿蒙 arm64 动态库 libcaotun.so 到 DevEco 工程 libs 目录。
# 前置: DevEco Studio（含 Native SDK），或设 OHOS_NDK 指向 SDK 的 native/ 目录。
# 产物: mobile/ohos/entry/libs/arm64-v8a/libcaotun.so（+ 同名 .h 供 NAPI 对照）
set -e
ROOT="$(cd "$(dirname "$0")/../.." && pwd)"

NATIVE="${OHOS_NDK:-}"
if [ -z "$NATIVE" ]; then
  for d in "/c/Program Files/Huawei/DevEco Studio/sdk/default/openharmony/native" \
           "$LOCALAPPDATA/Huawei/Sdk/default/openharmony/native" \
           "$HOME/Library/OpenHarmony/Sdk/default/openharmony/native"; do
    [ -d "$d/llvm/bin" ] && NATIVE="$d" && break
  done
fi
[ -z "$NATIVE" ] && { echo "错误: 未找到 OHOS NDK，请设 OHOS_NDK 指向 SDK 的 native 目录"; exit 1; }
echo "NDK: $NATIVE"

CLANG="$NATIVE/llvm/bin/aarch64-unknown-linux-ohos-clang"
case "$(uname -s)" in
*MINGW*|*MSYS*|*CYGWIN*)
  # Windows：包装脚本虽是 sh 文件（git-bash 能跑，go exec 不行），改 clang.exe + 显式
  # target/sysroot；路径转 8.3 短路径与正斜杠，规避 "Program Files" 空格拆词
  CC="$(cygpath -m "$(cygpath -d "$NATIVE/llvm/bin/clang.exe")") --target=aarch64-linux-ohos --sysroot=$(cygpath -m "$(cygpath -d "$NATIVE/sysroot")")"
  ;;
*)
  CC="$CLANG"  # macOS/Linux：目标包装脚本可直接执行
  ;;
esac
HILOG_SO="$ROOT/tools/hiloglib/libhilog_ndk.z.so"
case "$(uname -s)" in
*MINGW*|*MSYS*|*CYGWIN*) HILOG_SO="$(cygpath -m "$HILOG_SO")" ;;
esac

OUT_DIR="$ROOT/mobile/ohos/entry/libs/arm64-v8a"
mkdir -p "$OUT_DIR"

# 必须用 OpenHarmony-SIG 的 ohos-go fork（GOOS=openharmony，TLSDESC）：
# 上游 Go 的 c-shared 给 runtime TLS 写死 initial-exec 模型 + STATIC_TLS 标志，
# 鸿蒙 musl 拒绝在 dlopen 场景解析（报 initial-exec TLS resolves to dynamic definition）。
# fork 工具链构建: cd tools/ohos-go124/src && GOROOT_BOOTSTRAP=<本机go> make.bat
GOEXE=""
case "$(uname -s)" in
*MINGW*|*MSYS*|*CYGWIN*) GOEXE=".exe" ;;
esac
OHOS_GO="${OHOS_GO:-$ROOT/tools/ohos-go124}"
[ -x "$OHOS_GO/bin/go$GOEXE" ] || { echo "错误: 未找到 ohos-go 工具链 $OHOS_GO/bin/go$GOEXE"; echo "  构建: git clone -b release-branch.go1.24 https://gitcode.com/openharmony-sig/ohos_golang_go tools/ohos-go124 && cd tools/ohos-go124/src && GOROOT_BOOTSTRAP=\$(go env GOROOT) make.bat"; exit 1; }

cd "$ROOT/src"
GOROOT="$OHOS_GO" GOTOOLCHAIN=local \
GOOS=openharmony GOARCH=arm64 CGO_ENABLED=1 CC="$CC" CGO_CFLAGS="-D__MUSL__" CGO_LDFLAGS="$HILOG_SO" \
  "$OHOS_GO/bin/go$GOEXE" build -trimpath -ldflags="-s -w -extldflags=-Wl,-soname,libcaotun.so" -buildmode=c-shared -o "$OUT_DIR/libcaotun.so" ./mobile/ohos
echo "产物: $OUT_DIR/libcaotun.so"
ls -lh "$OUT_DIR/libcaotun.so"
