#!/bin/sh
# 一键准备 libcaotun.so 的全部构建依赖(幂等,可重复执行)。
# 完成后即可: sh scripts/shell/build-ohos.sh
# 前置: 本机已装 Go(bootstrap 用)与 DevEco Studio(NDK);git 可访问 gitcode.com
set -e
ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
cd "$ROOT"
PY=py
case "$(uname -s)" in *MINGW*|*MSYS*|*CYGWIN*) ;; *) PY=python3 ;; esac

echo "== [1/4] ohos-go fork 工具链(OpenHarmony-SIG,GOOS=openharmony/TLSDESC) =="
GOEXE=""
case "$(uname -s)" in *MINGW*|*MSYS*|*CYGWIN*) GOEXE=".exe" ;; esac
OHOS_GO="$ROOT/tools/ohos-go124"
if [ -x "$OHOS_GO/bin/go$GOEXE" ]; then
  echo "已存在,跳过"
else
  git clone --depth 1 -b release-branch.go1.24 \
    https://gitcode.com/openharmony-sig/ohos_golang_go "$OHOS_GO"
  echo "-- 自举编译工具链(首次约 5-15 分钟) --"
  BOOT="$(go env GOROOT)"
  cd "$OHOS_GO/src"
  case "$(uname -s)" in
  *MINGW*|*MSYS*|*CYGWIN*) cmd //c "cd /d %CD% && set GOROOT_BOOTSTRAP=$BOOT&& call .\make.bat" ;;
  *) GOROOT_BOOTSTRAP="$BOOT" ./make.bash ;;
  esac
  cd "$ROOT"
fi
"$OHOS_GO/bin/go$GOEXE" version

echo "== [2/4] gvisor 引擎依赖(sagernet fork vendor 副本) =="
# 上游 gvisor 的包名被上游自己弄坏且缺 bazel 生成物,不可用;
# sagernet fork(20250811)自包含,sing-box 生产验证;go 指令需压到 1.24 适配 fork 工具链
GV="$ROOT/tools/gvisor"
if [ -f "$GV/go.mod" ]; then
  echo "已存在,跳过"
else
  mkdir -p "$GV"
  ZIP="$ROOT/tools/gvisor.zip"
  curl -sL -o "$ZIP" "https://goproxy.cn/github.com/sagernet/gvisor/@v/v0.0.0-20250811-sing-box-mod.1.zip"
  $PY - "$ZIP" "$GV" <<'PYEOF'
import sys, zipfile, os, shutil
zip_path, dest = sys.argv[1], sys.argv[2]
with zipfile.ZipFile(zip_path) as z:
    prefix = z.namelist()[0].split('/')[0] + '/'
    for info in z.infolist():
        if info.is_dir():
            continue
        rel = info.filename[len(prefix):]
        target = os.path.join(dest, rel)
        os.makedirs(os.path.dirname(target), exist_ok=True)
        with z.open(info) as src, open(target, 'wb') as out:
            shutil.copyfileobj(src, out)
# go 指令与依赖压到 ohos-go(go1.24)可用的版本
gm = os.path.join(dest, 'go.mod')
s = open(gm, encoding='utf-8').read()
s = s.replace('go 1.25.0', 'go 1.24.0')
s = s.replace('golang.org/x/sys v0.43.0', 'golang.org/x/sys v0.26.0')
s = s.replace('golang.org/x/time v0.15.0', 'golang.org/x/time v0.7.0')
open(gm, 'w', encoding='utf-8', newline='\n').write(s)
PYEOF
  rm -f "$ZIP"
  chmod -R u+w "$GV" 2>/dev/null || true
fi
grep -q "go 1.24.0" "$GV/go.mod" || { echo "错误: tools/gvisor/go.mod 未指向 go1.24"; exit 1; }

echo "== [3/4] hilog 链接库(从 NDK sysroot 复制,Windows clang -l 搜索不可用) =="
NATIVE="${OHOS_NDK:-}"
if [ -z "$NATIVE" ]; then
  for d in "/c/Program Files/Huawei/DevEco Studio/sdk/default/openharmony/native" \
           "$LOCALAPPDATA/Huawei/Sdk/default/openharmony/native"; do
    [ -d "$d/llvm/bin" ] && NATIVE="$d" && break
  done
fi
HL="$ROOT/tools/hiloglib/libhilog_ndk.z.so"
if [ -f "$HL" ]; then
  echo "已存在,跳过"
elif [ -n "$NATIVE" ] && [ -f "$NATIVE/sysroot/usr/lib/aarch64-linux-ohos/libhilog_ndk.z.so" ]; then
  mkdir -p "$ROOT/tools/hiloglib"
  cp "$NATIVE/sysroot/usr/lib/aarch64-linux-ohos/libhilog_ndk.z.so" "$HL"
else
  echo "警告: 未找到 libhilog_ndk.z.so(装好 DevEco 后重跑)"
fi

echo "== [4/4] 完成 =="
echo "下一步: sh scripts/shell/build-ohos.sh"
