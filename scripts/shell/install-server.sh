#!/bin/sh
# caotun 服务端一键部署引导脚本（在服务器上运行，普通用户即可；systemd 自启需要 sudo）
#
# 新用户三步：
#   1) 上传发布包:  scp -r dist 用户@服务器IP:~/caotun-deploy
#   2) 运行引导:    ssh 用户@服务器IP 'sh ~/caotun-deploy/shell/install-server.sh'
#   3) 按提示回答   （端口/密码/是否签发真证书/是否开机自启，一路回车=全默认）
#
# 所有文件都装在当前用户家目录下: ~/caotun、~/server.conf、~/.caotun/（数据）
#
# 支持环境变量跳过对应交互（全自动部署用）:
#   PASS=密码  PORT=443  WS_PORT=8443  MAX_GB=20  QUOTA_DAYS=30
#   CERT_DOMAINS="域名1 域名2"          # 要签发真证书的域名（空=跳过，用自签兜底）
#   CF_Token/CF_Account_ID/CF_Zone_ID   # 有则走 DNS-01（橙云域名可用）；CERT_HTTP=1 则走 HTTP-01（仅灰云）
#   AUTOSTART=systemd|cron|no           # 开机自启方式（systemd 需 sudo 权限）
#   INSTALL_YES=1                       # 全部取默认值，不再询问

set -u
HOME_DIR="$HOME"

# ---------- 0) 定位发布包文件：本地摆放 > 从 GitHub Release 下载 ----------
GH_REPO="${CAOTUN_REPO:-xxff-dot/caotun}"
GH_MIRROR="${GH_MIRROR:-https://gh-proxy.com/}"   # GitHub 镜像前缀（直连失败自动兜底），可用环境变量覆盖
DIR="$(cd "$(dirname "$0")" && pwd)"
BIN=""
for c in "$DIR/../caotun_linux" "$DIR/../caotun" "$DIR/caotun_linux" "$DIR/caotun" "$HOME_DIR/caotun"; do
  if [ -f "$c" ]; then BIN="$c"; break; fi
done
find_script() {
  for c in "$DIR/$1" "$DIR/../$1"; do [ -f "$c" ] && { echo "$c"; return; }; done
}
START_SRC=$(find_script start-server.sh); STOP_SRC=$(find_script stop-server.sh)
if [ -z "$BIN" ] || [ -z "$START_SRC" ] || [ -z "$STOP_SRC" ]; then
  # 本地不完整 → 从 GitHub Release 下载（caotun-linux.tar.gz），国内直连失败自动走镜像
  echo "本地未找到完整发布文件，从 GitHub Release 下载 ($GH_REPO)..."
  PKG="$HOME_DIR/caotun-linux.tar.gz"
  fetch() {
    command -v curl >/dev/null 2>&1 && { curl -fsSL --connect-timeout 15 -o "$2" "$1" && return 0; }
    command -v wget >/dev/null 2>&1 && { wget -q -T 15 -O "$2" "$1" && return 0; }
    return 1
  }
  REL="https://github.com/$GH_REPO/releases/latest/download/caotun-linux.tar.gz"
  fetch "$REL" "$PKG" || fetch "$GH_MIRROR$REL" "$PKG" || { echo "错误: 下载失败，请手动上传 dist/ 后重跑"; exit 1; }
  DIR="$HOME_DIR/caotun-pkg"
  rm -rf "$DIR"; mkdir -p "$DIR"
  tar -xzf "$PKG" -C "$DIR"
  BIN="$DIR/caotun_linux"
  START_SRC="$DIR/shell/start-server.sh"; STOP_SRC="$DIR/shell/stop-server.sh"; CERT_SRC="$DIR/shell/issue-cert.sh"
  [ -f "$BIN" ] && [ -f "$START_SRC" ] || { echo "错误: 下载的包内容不完整"; exit 1; }
fi
CERT_SRC=${CERT_SRC:-$(find_script issue-cert.sh)}

# 仅支持 root：绑定 443/80 特权端口与 systemd 自启都需要
[ "$(id -u)" = "0" ] || { echo "错误: 请用 root 运行（普通用户无权绑定特权端口）"; exit 1; }

# ---------- 1) 采集配置（环境变量 > 交互输入 > 默认） ----------
ask() { # ask 提示 默认值 → $REPLY
  if [ "${INSTALL_YES:-}" = "1" ]; then REPLY="$2"; return; fi
  printf "%s [%s]: " "$1" "$2"
  read -r r
  REPLY="${r:-$2}"
}

ask "TLS 隧道端口" "${PORT:-443}";        PORT="$REPLY"
ask "WS/CDN 接入端口（也是管理 API 端口）" "${WS_PORT:-8443}"; WS_PORT="$REPLY"
ask "流量配额 GB（0 不限）" "${MAX_GB:-20}";  MAX_GB="$REPLY"
ask "配额周期天数（0 不限）" "${QUOTA_DAYS:-30}"; QUOTA_DAYS="$REPLY"

PASS="${PASS:-}"
if [ -z "$PASS" ] && [ "${INSTALL_YES:-}" != "1" ]; then
  printf "认证密码（回车=自动生成随机密码）: "
  read -r PASS
fi
if [ -z "$PASS" ]; then
  PASS=$(od -An -N16 -tx1 /dev/urandom | tr -d ' \n')
fi

CERT_DOMAINS="${CERT_DOMAINS:-}"
CERT_HTTP="${CERT_HTTP:-}"
if [ "${INSTALL_YES:-}" != "1" ] && [ -z "$CERT_DOMAINS" ] && [ -z "$CERT_HTTP" ]; then
  printf "签发真证书？输入域名（空格分隔，回车=跳过，用自签兜底）: "
  read -r CERT_DOMAINS
fi

AUTOSTART="${AUTOSTART:-}"
if [ -z "$AUTOSTART" ]; then
  DEF=systemd; command -v systemctl >/dev/null 2>&1 || DEF=cron
  if [ "${INSTALL_YES:-}" != "1" ]; then
    ask "开机自启方式 systemd/cron/no" "$DEF"; AUTOSTART="$REPLY"
  else
    AUTOSTART="$DEF"
  fi
fi

# ---------- 2) 安装文件（统一收进 ~/caotun/，数据在 ~/.caotun/） ----------
APP_DIR="$HOME_DIR/caotun"
mkdir -p "$APP_DIR" "$HOME_DIR/.caotun"
cp -f "$BIN" "$APP_DIR/caotun" && chmod +x "$APP_DIR/caotun"
cp -f "$START_SRC" "$APP_DIR/start-caotun.sh"
cp -f "$STOP_SRC" "$APP_DIR/stop-caotun.sh"
chmod +x "$APP_DIR/start-caotun.sh" "$APP_DIR/stop-caotun.sh"
[ -n "$CERT_SRC" ] && cp -f "$CERT_SRC" "$APP_DIR/issue-cert.sh"

cat > "$APP_DIR/server.conf" <<EOF
# caotun 服务端配置（由 install-server.sh 生成；改后 sh ~/caotun/start-caotun.sh 重启生效）
HOST=0.0.0.0
PORT=$PORT
WS_PORT=$WS_PORT
MAX_GB=$MAX_GB
QUOTA_DAYS=$QUOTA_DAYS
EOF

# 密码落盘（here 写入防密码进进程列表）
printf %s "$PASS" > "$HOME_DIR/.caotun/auth"
chmod 600 "$HOME_DIR/.caotun/auth"

# ---------- 3) 签发真证书（可选；写 数据目录/fullchain.pem，服务端自动热加载） ----------
CERT_NOTE="未签发（自签兜底，客户端走 TOFU）"
if [ -n "$CERT_DOMAINS" ]; then
  if [ -n "${CF_Token:-}" ] || [ -n "${CF_Key:-}" ]; then
    sh "$APP_DIR/issue-cert.sh" $CERT_DOMAINS && CERT_NOTE="已签发（$CERT_DOMAINS）" || CERT_NOTE="签发失败（稍后可重跑 ~/caotun/issue-cert.sh）"
  elif [ "${CERT_HTTP:-}" = "1" ]; then
    sh "$APP_DIR/issue-cert.sh" $CERT_DOMAINS --http && CERT_NOTE="已签发（$CERT_DOMAINS，HTTP-01）" || CERT_NOTE="签发失败（稍后可重跑 ~/caotun/issue-cert.sh）"
  else
    CERT_NOTE="未签发：缺 CF 凭据。补齐后运行: CF_Token=xxx CF_Account_ID=xxx CF_Zone_ID=xxx sh ~/caotun/issue-cert.sh $CERT_DOMAINS"
  fi
fi

# ---------- 4) 启动 + 开机自启 ----------
sh "$APP_DIR/stop-caotun.sh" 2>/dev/null
if [ "$AUTOSTART" = "systemd" ] && command -v systemctl >/dev/null 2>&1; then
  tee /etc/systemd/system/caotun.service > /dev/null <<EOF
[Unit]
Description=caotun tunnel server
After=network-online.target

[Service]
Environment=CAOTUN_HOME=$HOME_DIR/.caotun
WorkingDirectory=$APP_DIR
ExecStart=$APP_DIR/caotun server -host 0.0.0.0 -port $PORT -ws-port $WS_PORT -max-gb $MAX_GB -quota-days $QUOTA_DAYS
Restart=always
RestartSec=3

[Install]
WantedBy=multi-user.target
EOF
  systemctl daemon-reload
  systemctl enable --now caotun
  sleep 2
  RUN_MSG="systemd 管理：systemctl status/restart/stop caotun"
elif [ "$AUTOSTART" = "cron" ]; then
  (crontab -l 2>/dev/null | grep -v start-caotun.sh; echo '@reboot sh '"$APP_DIR"'/start-caotun.sh') | crontab -
  sh "$APP_DIR/start-caotun.sh"
  RUN_MSG="cron @reboot 自启；日常用 sh ~/caotun/start-caotun.sh 与 ~/caotun/stop-caotun.sh"
else
  AUTOSTART="no"
  sh "$APP_DIR/start-caotun.sh"
  RUN_MSG="无自启；日常用 sh ~/caotun/start-caotun.sh 与 ~/caotun/stop-caotun.sh"
fi
sleep 2

# ---------- 5) 部署摘要 ----------
echo ""
echo "==================== 部署完成 ===================="
echo "隧道地址   : 服务器IP:$PORT（直连线路，域名建议灰云解析）"
echo "CDN 地址   : 域名:$WS_PORT（橙云解析 + CF 代理，客户端 -ws）"
echo "认证密码   : $PASS"
echo "证书       : $CERT_NOTE"
echo "本地客户端 : 运行 dist/caotun.exe（或 start-web.bat 面板），配置卡填上面的地址与密码"
echo "服务端管理 : $RUN_MSG"
echo "流量/证书/密码可视化: 本地面板经管理 API 查看（端口 $WS_PORT）"
echo "提示       : 安全组放行 TCP $PORT 与 $WS_PORT；高丢包线路建议开 BBR"
echo "=================================================="
