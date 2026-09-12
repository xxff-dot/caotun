#!/bin/sh
# caotun 客户端启动（Windows/Linux/macOS 通用）：启动配置读本脚本同目录的 client.conf
# Ctrl+C 退出会自动还原系统代理（仅 Windows 接管过系统代理）；停止优先在本窗口按 Ctrl+C
# 本脚本在 仓库scripts/ 或 dist/（发布包） 内均可运行
DIR="$(cd "$(dirname "$0")" && pwd)"

# ---- 启动配置 ----
CONF="$DIR/../client.conf"
[ -f "$CONF" ] || { echo "未找到配置文件 $CONF"; exit 1; }
. "$CONF"
DIRECT_ADDR="${DIRECT_ADDR:-}"
AUTH_PASSWORD="${AUTH_PASSWORD:-}"
HOST="${HOST:-}"
LOCAL_PORT="${LOCAL_PORT:-21878}"
SYS_PROXY="${SYS_PROXY:-off}"
WS="${WS:-}"
INSECURE="${INSECURE:-}"
PAC_PORT="${PAC_PORT:-}"
[ -n "$DIRECT_ADDR" ] || { echo "client.conf 缺少 DIRECT_ADDR"; exit 1; }

# ---- 停旧实例 ----
pkill -x caotun >/dev/null 2>&1
sleep 1

# ---- 定位二进制：脚本同目录 → dist 根（发布包形态）→ 仓库 dist/（源码形态） ----
for CAND in "$DIR" "$DIR/.." "$DIR/../../dist"; do
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

# ---- 密码：配置里的固定密码（必填，需与服务端一致） ----
[ -n "$AUTH_PASSWORD" ] || { echo "client.conf 未设置 AUTH_PASSWORD，请自行设置认证密码（与服务端一致）"; exit 1; }
mkdir -p "$HOME/.caotun"
printf %s "$AUTH_PASSWORD" > "$HOME/.caotun/auth"

# ---- 可选参数拼装 ----
ARGS="client -server-addr $DIRECT_ADDR -lport $LOCAL_PORT -sysproxy $SYS_PROXY"
[ -n "$HOST" ] && ARGS="$ARGS -lhost $HOST"
[ "$WS" = "1" ] && ARGS="$ARGS -ws"
[ "$INSECURE" = "1" ] && ARGS="$ARGS -insecure"
[ -n "$PAC_PORT" ] && ARGS="$ARGS -pac-port $PAC_PORT"

exec "$BIN" $ARGS
