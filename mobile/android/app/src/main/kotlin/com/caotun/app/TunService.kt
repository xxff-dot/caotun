package com.caotun.app

import android.content.Context
import android.content.Intent
import android.net.VpnService
import android.os.ParcelFileDescriptor
import mobile.Mobile
import java.io.File

class TunService : VpnService() {

    companion object {
        private var tunInterface: ParcelFileDescriptor? = null
        var instance: TunService? = null
            private set
        const val ACTION_STOP = "com.caotun.app.STOP"

        fun stopAll(ctx: Context) {
            Mobile.stopTun()
            val i = Intent(ctx, TunService::class.java)
            i.action = ACTION_STOP
            ctx.startService(i)
        }
    }

    private var tunInterface: ParcelFileDescriptor? = null
    @Volatile
    private var working = false

    override fun onCreate() {
        super.onCreate()
        instance = this
    }

    override fun onStartCommand(intent: Intent?, flags: Int, startId: Int): Int {
        if (intent?.action == ACTION_STOP) {
            stopEngine()
            stopSelf()
            return START_NOT_STICKY
        }
        val channelId = "caotun_vpn"
        val channel = android.app.NotificationChannel(channelId, "caotun VPN", android.app.NotificationManager.IMPORTANCE_LOW)
        getSystemService(android.app.NotificationManager::class.java).createNotificationChannel(channel)
        val notification = android.app.Notification.Builder(this, channelId)
            .setContentTitle("caotun")
            .setContentText("全局代理运行中")
            .setSmallIcon(android.R.drawable.ic_lock_lock)
            .build()
        startForeground(1, notification)
        if (!working) {
            working = true
            Thread { // 路由表构建 + establish 较重，放子线程防 ANR
                try {
                    startEngine()
                } catch (e: Exception) {
                    android.util.Log.e("CaotunVpn", "startEngine failed", e)
                } finally {
                    working = false
                }
            }.start()
        }
        return START_STICKY
    }

    override fun onDestroy() {
        stopEngine()
        instance = null
        super.onDestroy()
    }

    override fun onRevoke() {
        stopEngine()
        stopSelf()
    }

    private fun startEngine() {
        tunInterface?.close()
        val sp = getSharedPreferences("caotun_cfg", MODE_PRIVATE)
        val server = sp.getString("server", "") ?: return
        val pass = sp.getString("pass", "") ?: return
        if (pass.isEmpty()) return
        val ws = sp.getBoolean("ws", false)
        val dns = sp.getString("dns", "223.5.5.5") ?: "223.5.5.5"
        val dialIp = sp.getString("dialip", "") ?: ""
        val dir = cacheDir.path + "/caotun"

        val serverIp = dialIp.ifEmpty {
            try {
                java.net.InetAddress.getByName(server.substringBefore(':')).hostAddress ?: ""
            } catch (e: Exception) {
                ""
            }
        }

        // 线路切换时重置 TOFU 指纹：直连(源站证书)与 CDN(CF 证书)指纹不同，
        // 共用一个指纹文件会误报"证书变化"。按服务器记录，换了线路就清指纹重新信任。
        val lastServer = sp.getString("last_server", "")
        if (lastServer != server) {
            File(dir, "fingerprint.txt").delete()
            sp.edit().putString("last_server", server).apply()
        }

        // 路由 = 「全部地址」减去（CN 段 + 保留段 + 服务器 IP）后的补集：
        // 国外流量进 tun 走隧道；国内/局域网/服务器目的地按普通路由走物理网。
        // 不用 protect 打标（EMUI 会拦 VPN 应用自身打标 TCP），也不做豁免。
        // 实现为对排除区间求补集，逐块 CIDR addRoute。
        val builder = Builder()
            .setSession("caotun")
            .addAddress("10.111.0.1", 30)
            .addDnsServer("10.111.0.2") // DNS 哨兵必须用 /30 对端地址:发给本机地址的包不进 tun,引擎收不到查询(鸿蒙同款)
            .setMtu(1400)
        val excluded = mutableListOf<Pair<Long, Long>>() // [start, end] 闭区间
        excluded.add(ipRange("0.0.0.0", 8))
        excluded.add(ipRange("10.0.0.0", 8))
        excluded.add(ipRange("127.0.0.0", 8))
        excluded.add(ipRange("169.254.0.0", 16))
        excluded.add(ipRange("172.16.0.0", 12))
        excluded.add(ipRange("192.168.0.0", 16))
        excluded.add(ipRange("224.0.0.0", 4))
        excluded.add(ipRange("240.0.0.0", 4))
        if (serverIp.isNotEmpty()) {
            val o = serverIp.split(".").map { it.toInt() }
            var ip = 0L
            for (v in o) ip = (ip shl 8) or v.toLong()
            excluded.add(Pair(ip, ip))
        }
        try {
            // 聚合到 /16 粒度：6346 条细粒度段会让路由 parcel 超 Binder 1MB 上限
            // （establish 抛 TransactionTooLargeException）。/16 聚合后约 2k 条，
            // 精度损失仅影响个别边缘 IP 的线路选择；引擎侧 cn_cidr 仍是全量精确表。
            val seen = HashSet<Int>()
            val lines = resources.openRawResource(R.raw.cn_cidr).bufferedReader().readLines()
            for (line in lines) {
                val t = line.substringBefore('#').trim()
                if (t.isEmpty()) continue
                val parts = t.split("/")
                if (parts.size != 2) continue
                val pfx = parts[1].trim().toIntOrNull() ?: continue
                if (pfx !in 1..32) continue
                val (bs, be) = ipRange(parts[0].trim(), pfx)
                // 段可能跨多个 /16（如 /13 聚合段），逐个 /16 枚举覆盖
                var k16 = ((bs shr 16) and 0xFFFFL).toInt()
                val endK = ((be shr 16) and 0xFFFFL).toInt()
                while (k16 <= endK) {
                    seen.add(k16)
                    k16++
                }
            }
            for (k in seen) {
                val base = (k.toLong() shl 8) * 256
                excluded.add(Pair(base, base + 65535))
            }
            android.util.Log.i("CaotunVpn", "CN /16 段 ${seen.size} 个已并入排除列表")
        } catch (e: Exception) {
            android.util.Log.w("CaotunVpn", "CN 段表读取失败，退化为全量隧道", e)
        }
        excluded.sortWith(compareBy({ it.first }, { it.second }))
        // 合并重叠/相邻区间
        val merged = mutableListOf<Pair<Long, Long>>()
        for (iv in excluded) {
            val last = merged.lastOrNull()
            if (last != null && iv.first <= last.second + 1) {
                if (iv.second > last.second) merged[merged.size - 1] = Pair(last.first, iv.second)
            } else {
                merged.add(iv)
            }
        }
        // DNS 哨兵网段(10.111.0.0/30)必须留在 tun 内：它落在 10.0.0.0/8 排除段里，
        // 不挖洞的话系统到哨兵的 DNS 包进不了隧道，DNS 全挂
        val wl = ipRange("10.111.0.0", 30)
        val carved = mutableListOf<Pair<Long, Long>>()
        for ((s, e) in merged) {
            val wlS = wl.first
            val wlE = wl.second
            if (wlE < s || wlS > e) {
                carved.add(Pair(s, e))
                continue
            }
            if (s < wlS) carved.add(Pair(s, wlS - 1))
            if (wlE < e) carved.add(Pair(wlE + 1, e))
        }
        // 补集：合并区间之间的空隙逐块 CIDR 化 addRoute
        var routes = 0
        var cur = 0L
        val total = 1L shl 32
        for (iv in carved) {
            if (iv.first > cur) routes += emitCidr(builder, cur, iv.first - 1)
            cur = iv.second + 1
            if (cur >= total) break
        }
        if (cur < total) routes += emitCidr(builder, cur, total - 1)
        android.util.Log.i("CaotunVpn", "路由块数=$routes")
        if (routes == 0) return // 理论不达：全被排除等于没有隧道

        val vpn = builder.establish() ?: return
        tunInterface = vpn
        val fd = vpn.detachFd()

        // dialIP 直拨免自解析；protectPath 留空；CN 段表交给引擎(双保险，路由层已分流)
        Mobile.startTun(
            server, pass, dir,
            fd.toLong(), 1400L, ws,
            "",
            dialIp, dir + "/cn_cidr.txt", dns
        )
    }

    private fun ipRange(addr: String, prefix: Int): Pair<Long, Long> {
        val o = addr.split(".").map { it.trim().toInt() }
        var ip = 0L
        for (v in o) ip = (ip shl 8) or v.toLong()
        val mask = ((-1L shl (32 - prefix)) and 0xFFFFFFFFL)
        val base = ip and mask
        return Pair(base, base + (1L shl (32 - prefix)) - 1)
    }

    /** 把对齐的地址区间 [start,end] 拆成最大 CIDR 块逐条 addRoute，返回条数 */
    private fun emitCidr(builder: Builder, start: Long, end: Long): Int {
        var a = start
        var count = 0
        while (a <= end) {
            var n = 0
            var x = a
            while (x and 1L == 0L && n < 32) {
                x = x ushr 1
                n++
            }
            var span = 1L shl n
            var pfx = 32 - n
            while (a + span - 1 > end) {
                span = span ushr 1
                pfx++
            }
            builder.addRoute(ipStr(a), pfx)
            count++
            a += span
            if (a == 0L) break // 2^32 溢出保护
        }
        return count
    }

    private fun ipStr(v: Long): String =
        "${(v ushr 24) and 0xff}.${(v ushr 16) and 0xff}.${(v ushr 8) and 0xff}.${v and 0xff}"

    private fun stopEngine() {
        Mobile.stopTun()
        tunInterface?.close()
        tunInterface = null
    }
}
