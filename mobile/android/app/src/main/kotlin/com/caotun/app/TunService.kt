package com.caotun.app

import android.content.Context
import android.content.Intent
import android.net.VpnService
import android.os.ParcelFileDescriptor
import mobile.Mobile

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

    fun protectFd(fd: Int): Boolean = protect(fd)

    override fun onCreate() {
        super.onCreate()
        instance = this
        ProtectServer.start(this)
    }

    override fun onStartCommand(intent: Intent?, flags: Int, startId: Int): Int {
        if (intent?.action == ACTION_STOP) {
            stopEngine()
            stopSelf()
            return START_NOT_STICKY
        }
        startEngine()
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
        val vpn = Builder()
            .setSession("caotun")
            .addAddress("10.111.0.1", 30)
            .addRoute("0.0.0.0", 0)
            .addDnsServer("10.111.0.1") // DNS 哨兵:进入 tun 由引擎劫持
            .setMtu(1400)
            .establish() ?: return
        tunInterface = vpn
        val fd = vpn.detachFd()

        val sp = getSharedPreferences("caotun_cfg", Context.MODE_PRIVATE)
        val server = sp.getString("server", "") ?: ""
        val pass = sp.getString("pass", "") ?: ""
        val ws = sp.getBoolean("ws", false)
        val dns = sp.getString("dns", "223.5.5.5") ?: "223.5.5.5"
        val dialIp = sp.getString("dialip", "") ?: ""
        val dir = cacheDir.path + "/caotun"

        Mobile.startTun(
            server, pass, dir,
            fd.toLong(), 1400L, ws,
            filesDir.path + "/protect.sock",
            dialIp, dir + "/cn_cidr.txt", dns
        )
    }

    private fun stopEngine() {
        Mobile.stopTun()
        tunInterface?.close()
        tunInterface = null
    }
}
