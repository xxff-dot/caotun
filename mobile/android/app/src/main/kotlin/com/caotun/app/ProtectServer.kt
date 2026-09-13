package com.caotun.app

import android.net.LocalServerSocket
import java.io.File

/**
 * protect 通道:Go 引擎拨号前把 fd 号(十进制文本)写到这个 unix socket,
 * 这里读出并调 VpnService.protect(fd) 打免捕获标记,回写 ok。
 */
object ProtectServer {
    private var server: LocalServerSocket? = null

    fun start(activity: TunService) {
        stop()
        val path = File(activity.filesDir, "protect.sock").absolutePath
        File(path).delete() // 清理上次进程残留,避免 bind 失败
        server = LocalServerSocket(path)
        Thread {
            while (true) {
                val conn = server?.accept() ?: break
                try {
                    val line = conn.inputStream.bufferedReader().readLine() ?: break
                    val fd = line.trim().toIntOrNull()
                    val ok = fd != null && activity.protectFd(fd)
                    conn.outputStream.write(if (ok) "ok".toByteArray() else "err".toByteArray())
                    conn.outputStream.flush()
                } catch (e: Exception) {
                    e.printStackTrace()
                } finally {
                    conn.close()
                }
            }
        }.start()
    }

    fun stop() {
        server?.close()
        server = null
    }
}
