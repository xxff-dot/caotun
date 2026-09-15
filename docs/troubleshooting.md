# 故障排查

## 排查入口

| 端 | 引擎日志 | 说明 |
|---|---|---|
| 安卓 | `adb shell run-as com.caotun.app cat cache/caotun/engine.log` | debug 包可读;含启动/连接/心跳/panic |
| 鸿蒙 | `hdc shell hilog -x --domain 0xC0A0 \| grep CaotunEngine` | 须带 `--domain 0xC0A0`,默认输出不含 app 域 |
| 服务端 | `~/.caotun/server.log` + `journalctl -u caotun` | 认证失败/隧道建立/关闭原因 |
| 实时 | `ss -tni dst <对端IP>`(服务端) | 看 bytes_retrans/dsack 判断链路丢包 |

## 常见症状 → 原因 → 处置

### 国外站点打不开,国内正常

1. 先看 App 连的是假 IP 还是真 IP:假 IP(198.18.x)= 白名单生效,问题在隧道;真 IP/毒 IP(fb/twitter 段)= DNS 没进引擎
2. DNS 没进引擎:安卓查哨兵是否 `10.111.0.2`(本机地址 `.1` 的包不进 tun);鸿蒙确认 `dnsAddresses: ['10.111.0.2']`
3. DNS 进了引擎但回真 IP:白名单没命中(查拼写/后缀),或引擎还是旧版本

### 引擎"越用越卡直至全部黑洞"

自环风暴:直连拨号的目标会被路由回 tun,无限嵌套拖死引擎。已修(哨兵网段拒绝 + 国外 IP 强制隧道);若重现,看 engine.log 的 `tun 写包失败` 和连接日志密度,以及服务端 `ss -tni` 的 retrans。

### 服务端日志大量"认证失败"

公网扫描器撞门。自己的出口 IP 被误封时:`protection.go` 封禁在内存里,重启服务端即清零。连接中断不算认证失败(已修),不会自我误伤。

### 特定网站 TLS 握手失败/证书错误

经隧道时对端返回的证书与域名不符 = 目标 IP 已不是该站点(常见于 GCP/Fastly 回收再分配的 IP)。确认解析路径是否被污染,以及白名单是否覆盖该域名。

### 隧道协议死锁(连接建立但无数据)

协议要求客户端发目标地址后**先等服务端状态码再发数据**。新增服务端分支时若直接读数据而不回状态码,两端互相等待直到超时(2026-09 dnsRelay 分支曾犯)。判据:服务端 socket `bytes_received` 只有握手字节、无应用数据。

### 系统加密 DNS(Private DNS/DoT)

手机开了 Private DNS 时,系统会对 VPN DNS IP 发 853 端口的 DoT。引擎对该网段 TCP 直接拒绝(RST),系统随即回落普通 53 查询,属正常日志噪音(`隧道已建立 10.111.0.2:853` 之类的连接不应再出现)。

## 真机自动化提示

- EMUI/鸿蒙 UI 状态可能来自残留 preferences(显示假绿灯),点击前先 `snapshot_display`/`screencap` 截图确认;点错一次 = 执行了相反操作
- 软键盘弹出会顶起布局,点按钮前先收键盘或用 `uitest dumpLayout` 取实时坐标
- 安卓 shell 的 `ping` 解析走 shell 自己的路径,结果不代表 App 流量;验证用浏览器 + 引擎日志
