#!/bin/sh
# caotun 服务端停止脚本（部署到服务器家目录下使用）
pkill -x caotun 2>/dev/null
echo "caotun 已停止"
