#!/bin/sh
# caotun 服务端卸载脚本（在服务器上运行；默认按当前用户家目录清理）
#
# 用法:
#   sh uninstall-server.sh                 # 卸载程序与自启，保留数据目录（密码/流量/证书）
#   KEEP_DATA=0 sh uninstall-server.sh     # 连数据目录一起删除（密码/证书/流量/日志，不可恢复）
#   PURGE_ACME=1 sh uninstall-server.sh    # 同时删除 acme.sh（~/.acme.sh 及其续期 cron）
set -u
HOME_DIR="$HOME"

# 停服务（systemd 安装形态；无权限时忽略失败）
if command -v systemctl >/dev/null 2>&1; then
  if [ "$(id -u)" = "0" ]; then
    systemctl stop caotun 2>/dev/null
    systemctl disable caotun 2>/dev/null
    rm -f /etc/systemd/system/caotun.service
    systemctl daemon-reload 2>/dev/null
  else
    sudo systemctl stop caotun 2>/dev/null
    sudo systemctl disable caotun 2>/dev/null
    sudo rm -f /etc/systemd/system/caotun.service
    sudo systemctl daemon-reload 2>/dev/null
  fi
fi
pkill -x caotun 2>/dev/null

# 去掉 cron 自启（若有）
if crontab -l >/dev/null 2>&1; then
  crontab -l | grep -v start-caotun.sh | crontab -
fi

# 删程序目录（二进制/启停脚本/issue-cert/server.conf 一体）
rm -rf "$HOME_DIR/caotun"

# 数据目录（默认保留）
if [ "${KEEP_DATA:-1}" = "0" ]; then
  rm -rf "$HOME_DIR/.caotun"
  echo "已删除数据目录 $HOME_DIR/.caotun"
else
  echo "已保留数据目录 $HOME_DIR/.caotun（密码/证书/流量）；彻底删除请用: KEEP_DATA=0 sh $0"
fi

# acme.sh（默认保留）
if [ "${PURGE_ACME:-0}" = "1" ]; then
  rm -rf "$HOME_DIR/.acme.sh"
  if crontab -l >/dev/null 2>&1; then
    crontab -l | grep -v "acme.sh --cron" | crontab -
  fi
  echo "已删除 acme.sh 及其续期 cron"
fi

echo "卸载完成"
