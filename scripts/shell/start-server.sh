#!/bin/sh
# caotun 服务端启动脚本（部署到服务器，与 server.conf 同目录）
# 用法: start-caotun.sh [密码] [-r]
#   默认: 服务端沿用现有密码（/root/.caotun/auth；首次由服务端自动生成）
#   密码: 使用指定密码（部署/测试场景经 "$(cat)" 传入，不进进程列表）
#   -r  : 经服务端 -rotate-pass 轮换为随机新密码
# 其余配置（端口/证书/流量配额/周期/监听）读 server.conf：
# 优先脚本同目录（服务器平铺部署 /root 形态），回退上一级（仓库 scripts/shell 与 dist/shell 形态）
DIR="$(cd "$(dirname "$0")" && pwd)"
if [ -f "$DIR/server.conf" ]; then
  . "$DIR/server.conf"
elif [ -f "$DIR/../server.conf" ]; then
  . "$DIR/../server.conf"
fi

PORT="${PORT:-443}"
WS_PORT="${WS_PORT:-8443}"
HOST="${HOST:-0.0.0.0}"
CERT="${CERT:-}"
KEY="${KEY:-}"
MAX_GB="${MAX_GB:-20}"
QUOTA_DAYS="${QUOTA_DAYS:-30}"
MAX_CONNS="${MAX_CONNS:-100}"
ROTATE="${ROTATE:-}"
AUTH="${AUTH:-}"

# 密码：$1 指定 > conf AUTH > 沿用 auth 文件；-r → 服务端 -rotate-pass 轮换
if [ "$1" = "-r" ]; then
  PASS_FLAG="-rotate-pass"
elif [ -n "$1" ]; then
  mkdir -p "$HOME/.caotun"
  printf %s "$1" > "$HOME/.caotun/auth"
  chmod 600 "$HOME/.caotun/auth"
elif [ -n "$AUTH" ]; then
  mkdir -p "$HOME/.caotun"
  printf %s "$AUTH" > "$HOME/.caotun/auth"
  chmod 600 "$HOME/.caotun/auth"
fi

pkill -x caotun 2>/dev/null
sleep 1
cd "$HOME" || exit 1

ARGS="server -host $HOST -port $PORT -ws-port $WS_PORT -max-gb $MAX_GB -quota-days $QUOTA_DAYS -max-conns $MAX_CONNS"
[ "$ROTATE" = "1" ] && ARGS="$ARGS -rotate-pass"
if [ -n "$CERT" ] && [ -n "$KEY" ]; then
  ARGS="$ARGS -cert $CERT -key $KEY"
fi
[ -n "$PASS_FLAG" ] && ARGS="$ARGS $PASS_FLAG"

# 二进制定位：脚本同目录（安装器布局 ~/caotun/）> 上一级（dist 形态）> 家目录
BIN=""
for c in "$DIR/caotun" "$DIR/../caotun" "$HOME/caotun"; do
  [ -x "$c" ] && { BIN="$c"; break; }
done
[ -n "$BIN" ] || { echo "错误: 找不到 caotun 二进制"; exit 1; }

# 日志由服务端自管（~/.caotun/server.log，5MB 自动轮转）；nohup 输出丢弃防日志爆盘
nohup "$BIN" $ARGS >/dev/null 2>&1 &
