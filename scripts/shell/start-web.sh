#!/bin/sh
# caotun 管理面板启动（Windows/Linux/macOS 通用）：先清理旧实例，浏览器打开面板
# 停止：在面板窗口里按 Ctrl+C（自动停止客户端并还原系统代理，Windows 下还原注册表代理）
# 本脚本在 仓库scripts/ 或 dist/（发布包） 内均可运行；端口等配置读同目录 web.conf
pkill -x caotun >/dev/null 2>&1
sleep 1

# 定位二进制：脚本同目录 → dist 根（发布包形态）→ 仓库 bin/（源码形态）
DIR="$(cd "$(dirname "$0")" && pwd)"
if [ -f "$DIR/../client.conf" ]; then . "$DIR/../client.conf"; fi
WEB_PORT="${WEB_PORT:-21877}"
for CAND in "$DIR" "$DIR/.." "$DIR/../../bin"; do
  if [ -x "$CAND/caotun_linux" ] || [ -x "$CAND/caotun_macos" ]; then
    DIR="$CAND"
    break
  fi
done
cd "$DIR" || exit 1
case "$(uname -s)" in
  Darwin) BIN=./caotun_macos ;;
  *)      BIN=./caotun_linux ;;
esac
[ -x "$BIN" ] || { echo "未找到可执行的 caotun 二进制（先跑 scripts/shell/build.sh）"; exit 1; }

# 面板就绪后自动打开浏览器
(
  sleep 2
  case "$(uname -s)" in
    Darwin) open  "http://127.0.0.1:$WEB_PORT" ;;
    *)      xdg-open "http://127.0.0.1:$WEB_PORT" >/dev/null 2>&1 ;;
  esac
) >/dev/null 2>&1 &

exec "$BIN" web -web-port "$WEB_PORT"
