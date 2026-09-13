package com.caotun.app

import android.content.Context
import android.content.Intent
import android.net.VpnService
import android.os.ParcelFileDescriptor

/**
 * VPN 服务:建立 tun 虚拟网卡,启动 Go 引擎(gomobile 绑定)。
 * protect 通道:引擎拨号前把 fd 号发到 filesDir/protect.sock,
 * 这里调 VpnService.protect(fd) 打免捕获标记,防止隧道流量回环。
 */
class TunService : VpnService() {

    companion object {
        private var tunInterface: ParcelFileDescriptor? = null
        var instance: TunService? = null
            private set

        /** 启动:授权流程完成后由 UI 调用。返回 0=成功, 其他=失败 */
        fun start(context: Context, cfg: Cfg, dialIp: String, dns: String): Int {
            val svc = instance ?: run {
                // 服务未起:先启动再等 onCreate
                context.startService(Intent(context, TunService::class.java))
                Thread.sleep(300)
                instance ?: return 3
            }
            return svc.doStart(cfg, dialIp, dns)
        }

        fun stopAll(context: Context) {
            Mobile.stopTun()
            instance?.stopSelf()
            ProtectServer.stop()
        }
    }

    private var tunInterface: ParcelFileDescriptor? = null

    override fun onCreate() {
        super.onCreate()
        instance = this
        ProtectServer.start(this)
    }

    override fun onDestroy() {
        super.onDestroy()
        Mobile.stopTun()
        ProtectServer.stop()
        tunInterface?.close()
        tunInterface = null
        instance = null
    }

    override fun onRevoke() {
        // 用户在系统设置里断开了 VPN
        Mobile.stopTun()
        ProtectServer.stop()
        tunInterface = null
        stopSelf()
    }

    private fun doStart(cfg: Cfg, dialIp: String, dns: String): Int {
        tunInterface?.close()
        val vpn = Builder()
            .setSession("caotun")
            .addAddress("10.111.0.1", 30)
            .addRoute("0.0.0.0", 0)
            .addDnsServer("10.111.0.1") // DNS 哨兵:进入 tun 由引擎劫持
            .setMtu(1400)
            .establish() ?: return 3
        tunInterface = vpn
        val fd = vpn.detachFd() // detach 后由 Go 引擎持有
        return mobile.Mobile.startTun(
            cfg.server, cfg.pass,
            cacheDir.path + "/caotun",
            fd.toLong(), 1400L, cfg.ws,
            filesDir.path + "/protect.sock",
            dialIp,
            cacheDir.path + "/caotun/cn_cidr.txt",
            dns
        )
    }
}
