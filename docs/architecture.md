# 架构

单二进制加密 TCP 转发隧道。`server` / `client` / `web` 三种模式共用 `src/main.go` 分发;移动端(安卓/鸿蒙)共用 `src/mobile/core` + `src/tun2sock` 引擎。

## 总体数据流

```
┌─ 白名单域名(google.com 等) ─────────────────────────────┐
│ App 问 DNS → 引擎秒回假IP(198.18.0.0/15)                │
│ App 连假IP → 引擎反查域名 → 域名原文进隧道(TLS/WS)        │
│            → VPS 解析并连接目标 → 回包原路返回            │
└─────────────────────────────────────────────────────────┘
┌─ 其余流量(baidu 等) ────────────────────────────────────┐
│ App 问 DNS → 引擎转发国内上游(223.5.5.5)解析真 IP        │
│ App 连真 IP → 直连(不经 VPS)                            │
└─────────────────────────────────────────────────────────┘
```

要点:白名单域名全程不做本机真实解析,GFW 对本机/国内 DNS 的污染无从生效;解析全部发生在 VPS(境外出口)。

## 隧道协议(每条连接一次握手)

1. TCP + TLS(客户端先标准 CA 校验,失败回退 TOFU 指纹)
2. 【WS 模式】TLS 之上再发 WebSocket Upgrade(`GET /tf`),CDN 可代理
3. 服务端 → 32 字节随机 nonce;客户端 → HMAC-SHA256(密码, nonce)
4. 客户端 → 目标地址(`protocol/` 编码,ATYP 支持 IPv4/域名);服务端 → 1 字节状态码(0 成功/1 失败/2 配额满)
5. 双向裸流转发;目标端口 53 走服务端 `dnsRelay`(本地 UDP 查询 + 60s 缓存)

关键函数:服务端握手 `src/server/server.go` `handleServerConn`;客户端 `src/tunnel/client.go` `finishTunnel`。**状态码必须在读目标数据前发出**,否则两端互相等待形成死锁(2026-09 已修的 bug)。

## 移动端引擎(src/tun2sock + src/mobile/core)

- **fake-ip 池**(`fakeip.go`):198.18.0.0/15 顺序分配,域名↔假 IP 双向映射,池满整体重建
- **DNS 劫持**(`handleDNS`):白名单命中回假 IP(A 记录);AAAA/HTTPS 回 NODATA(逼 IPv4+明文 SNI);未命中转发 `DirectResolvers`(默认 223.5.5.5,直连)
- **TCP 分流**(优先级从上到下):
  1. 假 IP → 反查域名 → `o.Dial(域名)` 进隧道
  2. 哨兵网段 10.111.0.0/30 的 TCP(如 DoT:853)→ 直接拒(防自环)
  3. `CNMatcher.DirectOK(ip)`(CN 公网/内网)→ `o.Direct` 直连
  4. 其余(国外裸 IP)→ `o.Dial` 走隧道(直连会自环!)
- **自环防线**:直连拨号只允许确定在 VPN 路由之外的地址(CN/内网);国外 IP 直连会被路由回 tun 无限嵌套,曾把引擎拖死
- CN 段表内嵌(`cn_cidr.txt` go:embed,数据源 gaoyifan/china-operator-ip),`Options.CIDRPath` 可选外部覆盖

## 移动端白名单(src/proxylist)

`proxylist.Defaults()` 默认 8 条(github/google/googleapis/gstatic/youtube/huggingface/android/dl.google.com);后缀匹配(点边界)。App「代理域名」编辑框写入 `cache/caotun/proxy_domains.txt`(一行一个),存在即完整替换默认;清空保存 = 恢复默认。改完断开重连生效。

## 服务端

- `handleServerConn`:nonce/HMAC 认证 → 读目标 → 状态码 → 转发;端口 53 特殊走 `dnsrelay.go`
- `protection.go`:IP 封禁(5 分钟滑窗 10 次失败封 30 分钟)、密码热轮换、流量配额熔断
- `admin.go`:`/_admin/*` 管理 API(X-Auth 鉴权),web 面板经它管理服务端
