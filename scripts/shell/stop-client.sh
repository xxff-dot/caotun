#!/bin/sh
# caotun 客户端停止（Linux/macOS）
# 注意：强杀不会还原系统代理；再跑一次 start-client.sh/start-web.sh 并 Ctrl+C 即可还原
pkill -x caotun && echo "caotun 已停止"
