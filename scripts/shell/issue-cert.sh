#!/bin/sh
# caotun 证书定时签发脚本（在服务器上运行；acme.sh 自带每日续期 cron，到期前自动续）
#
# 用法:
#   sh issue-cert.sh 域名1 [域名2 ...]        # DNS-01（推荐）：橙云域名也能签，不占端口
#   sh issue-cert.sh 域名1 --http             # HTTP-01 standalone（临时占 80 端口，仅灰云域名）
#
# 示例（直连 + CDN 域名签进同一张证书）:
#   CF_Token=xxx CF_Account_ID=xxx CF_Zone_ID=xxx sh issue-cert.sh direct.example.com cdn.example.com
#
# 前置: Cloudflare 控制台创建 API Token（权限 Zone.DNS Edit + Zone.Zone Read），
#       Account ID / Zone ID 在 CF 域名概览页右下角。
# 产出: ~/.caotun/fullchain.pem + privkey.pem（服务端检测到文件变化自动热加载，无需重启）
# 续期: acme.sh 安装时自带每日 cron，到期前自动续，续期后自动写回上述文件；
#       手动重签 = 重跑本脚本；手动续期 = ~/.acme.sh/acme.sh --renew -d 主域名 --force

DIR="$(cd "$(dirname "$0")" && pwd)"
DATA_DIR="${CAOTUN_HOME:-$HOME/.caotun}"
ACME="$HOME/.acme.sh/acme.sh"

[ $# -ge 1 ] || { echo "用法: sh $0 域名1 [域名2 ...] [--http]"; exit 1; }

HTTP_MODE=0
DOMAINS=""
for a in "$@"; do
  if [ "$a" = "--http" ]; then
    HTTP_MODE=1
  else
    DOMAINS="$DOMAINS $a"
  fi
done

# 1) acme.sh 就位（已装则跳过）
if [ ! -f "$ACME" ]; then
  echo "安装 acme.sh..."
  curl -sL "https://get.acme.sh" | sh -s "email=acct@outlook.com" >/dev/null 2>&1
  if [ ! -f "$ACME" ]; then
    echo "acme.sh 安装失败，请手动执行: curl https://get.acme.sh | sh -s email=我的邮箱"
    exit 1
  fi
  "$ACME" --set-default-ca --server letsencrypt
fi

mkdir -p "$DATA_DIR"

# 2) 签发（已有有效证书未到期时 acme.sh 会跳过，属正常，继续走安装）
MAIN=""
for a in $DOMAINS; do
  MAIN="$a"
  break
done
if [ "$HTTP_MODE" = "1" ]; then
  "$ACME" --issue --standalone -d $DOMAINS --server letsencrypt
else
  if [ -z "${CF_Key:-}" ] && [ -z "${CF_Token:-}" ]; then
    echo "缺少 Cloudflare 凭据: export CF_Key=全局API密钥 CF_Email=账号邮箱"
    echo "或         export CF_Token=API令牌 CF_Account_ID=... CF_Zone_ID=...（推荐，权限最小化）"
    exit 1
  fi
  "$ACME" --issue --dns dns_cf -d $DOMAINS --server letsencrypt
fi

# 3) 安装到数据目录（acme.sh 记住映射，每日 cron 续期后自动写回 → 服务端热加载）。
#    install-cert 成功 = 证书可用的唯一判据（--issue 跳过/告警不算失败）
"$ACME" --install-cert -d "$MAIN" \
  --fullchain-file "$DATA_DIR/fullchain.pem" \
  --key-file "$DATA_DIR/privkey.pem" \
  || { echo "失败: 证书签发/安装未成功，按上方 acme.sh 输出排查"; exit 1; }
echo "完成: 证书已写入 $DATA_DIR/fullchain.pem + privkey.pem（服务端自动热加载，无需重启）"
