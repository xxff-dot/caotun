package com.caotun.app

import android.app.Activity
import android.content.Intent
import android.net.VpnService
import android.os.Bundle
import androidx.activity.ComponentActivity
import androidx.activity.compose.setContent
import androidx.activity.result.contract.ActivityResultContracts
import androidx.compose.foundation.background
import androidx.compose.foundation.border
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.AlertDialog
import androidx.compose.material3.Button
import androidx.compose.material3.ButtonDefaults
import androidx.compose.material3.OutlinedTextField
import androidx.compose.material3.Surface
import androidx.compose.material3.Switch
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableIntStateOf
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import com.journeyapps.barcodescanner.ScanContract
import com.journeyapps.barcodescanner.ScanOptions
import kotlinx.coroutines.delay
import mobile.Mobile

private val C_BG = Color(0xFFF3F5F9)
private val C_CARD = Color(0xFFFFFFFF)
private val C_PRI = Color(0xFF3B82F6)
private val C_OK = Color(0xFF22C55E)
private val C_DANGER = Color(0xFFEF4444)
private val C_TEXT = Color(0xFF1B2230)
private val C_SUB = Color(0xFF8A93A6)

class MainActivity : ComponentActivity() {

    // ---------- 状态(构造期置空,onCreate 里加载;Context 未 attach 前不能读偏好) ----------
    private val configs = mutableStateOf<List<Cfg>>(emptyList())
    private val active = mutableIntStateOf(-1)
    private val running = mutableStateOf(false)
    private val status = mutableStateOf("Starting")
    private val lastErr = mutableStateOf("")
    private val netBlocked = mutableStateOf(false)
    private val dnsInput = mutableStateOf("223.5.5.5")
    private val domainsInput = mutableStateOf("")

    // 编辑器:null = 关闭;非 null = 正在编辑的原始配置
    private var editing by mutableStateOf<Cfg?>(null)
    private var editorIsNew by mutableStateOf(false)
    private var editorOldServer by mutableStateOf("")

    // 删除确认:null = 关闭
    private var delTarget by mutableStateOf<Cfg?>(null)

    private var lastStart = 0L
    private var transitionUntil = 0L
    private var vpnPending: ((Boolean) -> Unit)? = null

    private val vpnLauncher =
        registerForActivityResult(ActivityResultContracts.StartActivityForResult()) { r ->
            val granted = r.resultCode == Activity.RESULT_OK
            vpnPending?.invoke(granted)
            vpnPending = null
        }

    private val scanLauncher =
        registerForActivityResult(ScanContract()) { r ->
            val uri = r.contents ?: return@registerForActivityResult
            if (!uri.startsWith("caotun://")) {
                lastErr.value = "不是有效的 caotun 二维码"
                return@registerForActivityResult
            }
            val rest = uri.removePrefix("caotun://")
            val qi = rest.indexOf('?')
            val server = if (qi >= 0) rest.substring(0, qi) else rest
            var pass = ""
            var ws = false
            var dns = ""
            if (qi >= 0) {
                for (kv in rest.substring(qi + 1).split('&')) {
                    val i = kv.indexOf('=')
                    if (i < 0) continue
                    when (kv.substring(0, i)) {
                        "p" -> pass = java.net.URLDecoder.decode(kv.substring(i + 1), "UTF-8")
                        "ws" -> ws = kv.substring(i + 1) == "1"
                        "dns" -> dns = java.net.URLDecoder.decode(kv.substring(i + 1), "UTF-8")
                    }
                }
            }
            if (server.isEmpty() || pass.isEmpty()) {
                lastErr.value = "二维码缺少地址或密码"
                return@registerForActivityResult
            }
            if (dns.isNotBlank()) ConfigStore.saveDns(this@MainActivity, dns.trim()) // 写入既有偏好，下次启动生效
            upsertAndStart(Cfg(server, pass, ws))
        }

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        configs.value = ConfigStore.load(this)
        active.intValue = if (configs.value.isEmpty()) -1 else 0
        dnsInput.value = ConfigStore.dns(this)
        domainsInput.value = runCatching {
            java.io.File(cacheDir, "caotun/proxy_domains.txt").readText()
        }.getOrDefault("")
            .replace("\r", "")
            .split("\n")
            .map { it.trim() }
            .filter { it.isNotEmpty() && !it.startsWith("#") }
            .joinToString("\n")
        setContent { Dashboard() }
        // 启动自动连接:已有服务器配置就直接连,免每次手点
        // (首次会弹 VPN 授权框,授权后后续启动静默直连;引擎已在跑则不重复拉起)
        if (!mobile.Mobile.running()) configs.value.getOrNull(active.intValue)?.let { startVpn(it) }
    }

    // ---------- VPN 启停 ----------

    private fun startVpn(cfg: Cfg) {
        lastErr.value = ""
        status.value = "连接中..."
        lastStart = System.currentTimeMillis()
        transitionUntil = System.currentTimeMillis() + 8000
        // VPN 授权(首次;已授权时 prepare 返回 null 直接走 doStart)
        val prepare = VpnService.prepare(this)
        if (prepare != null) {
            vpnPending = { granted ->
                if (granted) doStart(cfg) else {
                    lastErr.value = "VPN 授权被拒绝"
                    status.value = "未连接"
                }
            }
            vpnLauncher.launch(prepare)
            return
        }
        doStart(cfg)
    }

    private fun doStart(cfg: Cfg) {
        lastStart = System.currentTimeMillis()
        running.value = true
        status.value = "已连接"
        // 预解析+起服务放子线程:getAllByName 在主线程抛 NetworkOnMainThreadException,
        // 之前 dialip 一直是空的就是这个原因
        Thread {
            var dialIp = ""
            try {
                val all = java.net.InetAddress.getAllByName(cfg.server.substringBefore(':'))
                dialIp = all.firstOrNull { it.address.size == 4 }?.hostAddress ?: ""
            } catch (e: Exception) {
                android.util.Log.w("CaotunUI", "pre-resolve failed: $e")
            }
            android.util.Log.i("CaotunUI", "dialip=$dialIp")
            // 联网自检:EMUI「应用联网管理」关掉 WLAN/数据后,应用 uid 的所有
            // TCP 都被系统层拦截(protect/豁免救不了),这里主动探测并引导用户放行
            netBlocked.value = !canProbeOut()
            if (netBlocked.value) {
                runOnUiThread {
                    lastErr.value = "无法联网:系统联网管理未放行 caotun(WLAN/数据),点此去开启 →"
                }
            }
            ConfigStore.mirrorActive(this, cfg)
            // 预解析的 IP 存给 TunService(VPN 激活后引擎用 IP 直拨避免 DNS 死锁)
            getSharedPreferences("caotun_cfg", MODE_PRIVATE).edit()
                .putString("dialip", dialIp).apply()
            startService(Intent(this, TunService::class.java))
        }.start()
    }

    // 应用 uid 出网探测:双上游任一连上即放行(拨 TCP DNS 53 口,国内公共 DNS 均支持)
    private fun canProbeOut(): Boolean {
        for (host in listOf("223.5.5.5", "119.29.29.29")) {
            try {
                val s = java.net.Socket()
                s.connect(java.net.InetSocketAddress(host, 53), 2000)
                s.close()
                return true
            } catch (e: Exception) {
            }
        }
        return false
    }

    private fun openAppNetSettings() {
        try {
            startActivity(
                Intent(
                    android.provider.Settings.ACTION_APPLICATION_DETAILS_SETTINGS,
                    android.net.Uri.fromParts("package", packageName, null)
                )
            )
        } catch (e: Exception) {
        }
    }

    private fun stopVpn() {
        TunService.stopAll(this)
        running.value = false
        status.value = "未连接"
    }

    private fun toggleVpn() {
        val cfg = configs.value.getOrNull(active.value)
        if (cfg == null) {
            lastErr.value = "请先扫码或添加服务器"
            return
        }
        if (running.value) stopVpn() else startVpn(cfg)
    }

    private fun upsertAndStart(cfg: Cfg) {
        val list = configs.value.toMutableList()
        val idx = list.indexOfFirst { it.server == cfg.server }
        if (idx >= 0) list[idx] = cfg else list.add(cfg)
        configs.value = list
        active.intValue = list.indexOf(cfg)
        ConfigStore.saveAll(this, list)
        ConfigStore.mirrorActive(this, cfg)
        ConfigStore.setAutostart(this, true)
        if (running.value) TunService.stopAll(this)
        startVpn(cfg)
    }

    // ---------- 编辑器 ----------

    private fun openEditor(cfg: Cfg?) {
        editorOldServer = cfg?.server ?: ""
        editorIsNew = cfg == null
        editing = cfg ?: Cfg("", "", false)
        editorShow.value = true
    }

    private fun saveEditor() {
        val old = editing ?: return
        val server = editorServer.value.trim()
        val pass = editorPass.value.trim()
        if (server.isEmpty() || pass.isEmpty()) {
            editorHint.value = "地址与密码必填"
            return
        }
        val list = configs.value.toMutableList()
        val idx = list.indexOfFirst { it.server == editorOldServer }
        if (idx >= 0) list[idx] = Cfg(server, pass, editorWs.value) else list.add(
            Cfg(server, pass, editorWs.value)
        )
        configs.value = list
        active.intValue = list.indexOfFirst { it.server == server }
        ConfigStore.saveAll(this, list)
        ConfigStore.mirrorActive(this, list.first { it.server == server })
        closeEditor()
        // 改了正在使用的配置 → 重连生效
        if (running.value) {
            TunService.stopAll(this)
            startVpn(list.first { it.server == server })
        }
    }

    private fun closeEditor() {
        editorShow.value = false
        editing = null
        editorServer.value = ""
        editorPass.value = ""
        editorWs.value = false
        editorHint.value = ""
    }

    // ---------- 编辑器状态 ----------
    private val editorShow = mutableStateOf(false)
    private val editorServer = mutableStateOf("")
    private val editorPass = mutableStateOf("")
    private val editorWs = mutableStateOf(false)
    private val editorHint = mutableStateOf("")

    // ---------- DNS ----------
    private fun saveDns() {
        val v = dnsInput.value.trim()
        if (v.isNotEmpty()) ConfigStore.saveDns(this, v)
    }

    // ---------- 代理域名白名单 ----------
    private fun saveDomains() {
        val dir = java.io.File(cacheDir, "caotun")
        if (!dir.exists()) dir.mkdirs()
        val lines = domainsInput.value.replace("\r", "").split("\n")
            .map { it.trim() }
            .filter { it.isNotEmpty() }
        java.io.File(dir, "proxy_domains.txt").writeText(lines.joinToString("\n"))
        lastErr.value = if (running.value) "白名单已保存(${lines.size} 条),断开重连后生效" else "白名单已保存(${lines.size} 条)"
    }

    // ---------- UI ----------

    @Composable
    private fun Dashboard() {
        // 心跳轮询:3 秒同步引擎状态;意外掉线自愈重启
        LaunchedEffect(Unit) {
            while (true) {
                delay(3000)
                val fresh = mobile.Mobile.running()
                if (fresh != running.value) {
                    running.value = fresh
                    if (fresh) {
                        status.value = "已连接"
                    } else {
                        status.value = "未连接"
                        if (System.currentTimeMillis() - lastStart > 20000 &&
                            ConfigStore.autostart(this@MainActivity)
                        ) {
                            configs.value.getOrNull(active.intValue)?.let { startVpn(it) }
                        }
                    }
                }
            }
        }

        Column(
            Modifier
                .fillMaxSize()
                .background(C_BG)
                .verticalScroll(rememberScrollState())
                .padding(horizontal = 16.dp)
        ) {
            Spacer(Modifier.height(16.dp))
            Text("caotun", fontSize = 26.sp, fontWeight = FontWeight.Bold, color = C_TEXT)

            Spacer(Modifier.height(12.dp))
            if (lastErr.value.isNotEmpty()) {
                Text(
                    lastErr.value, fontSize = 12.sp, color = C_DANGER,
                    modifier = if (netBlocked.value) {
                        Modifier.clickable { openAppNetSettings() }
                    } else Modifier
                )
                Spacer(Modifier.height(6.dp))
            }

            // 仪表卡:大圆钮
            Surface(
                shape = RoundedCornerShape(20.dp),
                color = C_CARD,
                shadowElevation = 4.dp,
                modifier = Modifier.fillMaxWidth()
            ) {
                Column(
                    horizontalAlignment = Alignment.CenterHorizontally,
                    modifier = Modifier.padding(vertical = 24.dp)
                ) {
                    val on = running.value
                    Box(
                        contentAlignment = Alignment.Center,
                        modifier = Modifier
                            .size(120.dp)
                            .background(if (on) C_OK else Color.Transparent, CircleShape)
                            .border(5.dp, if (on) C_OK else C_PRI, CircleShape)
                            .clickable { toggleVpn() }
                    ) {
                        Column(horizontalAlignment = Alignment.CenterHorizontally) {
                            Text("⏻", fontSize = 30.sp, color = if (on) Color.White else C_PRI)
                            Text(
                                if (on) "断开" else "连接",
                                fontSize = 13.sp,
                                color = if (on) Color.White else C_PRI
                            )
                        }
                    }
                    Spacer(Modifier.height(10.dp))
                    Text(
                        configs.value.getOrNull(active.intValue)?.server ?: "未选择服务器",
                        fontSize = 15.sp, fontWeight = FontWeight.Medium, color = C_TEXT,
                        maxLines = 1, overflow = TextOverflow.Ellipsis
                    )
                    Text(
                        if (running.value) "全局代理 · 加密隧道中" else "点击圆钮连接",
                        fontSize = 12.sp, color = C_SUB
                    )
                }
            }

            Spacer(Modifier.height(16.dp))

            // 服务器标题
            Row(
                Modifier.fillMaxWidth(),
                verticalAlignment = Alignment.CenterVertically
            ) {
                Text("服务器", fontSize = 15.sp, fontWeight = FontWeight.Bold, color = C_TEXT)
                Spacer(Modifier.weight(1f))
                Text(
                    "＋ 扫码", fontSize = 13.sp, color = C_PRI,
                    modifier = Modifier.clickable {
                        val opt = ScanOptions().setDesiredBarcodeFormats(ScanOptions.QR_CODE)
                            .setPrompt("对准 caotun 二维码")
                        scanLauncher.launch(opt)
                    }
                )
                Spacer(Modifier.width(8.dp))
                Text(
                    "｜", fontSize = 12.sp, color = C_SUB
                )
                Spacer(Modifier.width(8.dp))
                Text(
                    "＋ 手动", fontSize = 13.sp, color = C_PRI,
                    modifier = Modifier.clickable { openEditor(null) }
                )
            }
            Spacer(Modifier.height(8.dp))

            // 配置卡片列表(外层已 verticalScroll,这里用普通 Column 避免嵌套滚动)
            Column(verticalArrangement = Arrangement.spacedBy(8.dp)) {
                for (i in configs.value.indices) {
                    val c = configs.value[i]
                    val selected = i == active.intValue
                    Surface(
                        shape = RoundedCornerShape(14.dp),
                        color = C_CARD,
                        shadowElevation = 2.dp,
                        border = if (selected) androidx.compose.foundation.BorderStroke(1.dp, C_PRI) else null,
                        modifier = Modifier
                            .fillMaxWidth()
                            .clickable {
                                active.intValue = i
                                ConfigStore.mirrorActive(this@MainActivity, c)
                            }
                    ) {
                        Row(
                            Modifier.padding(12.dp),
                            verticalAlignment = Alignment.CenterVertically
                        ) {
                            Box(
                                Modifier
                                    .size(18.dp)
                                    .background(if (selected) C_PRI else Color.Transparent, CircleShape)
                                    .border(2.dp, if (selected) C_PRI else Color(0xFFC6CCD8), CircleShape)
                            )
                            Spacer(Modifier.width(10.dp))
                            Column(Modifier.weight(1f)) {
                                Text(
                                    c.server, fontSize = 15.sp,
                                    fontWeight = FontWeight.Medium, color = C_TEXT, maxLines = 1
                                )
                                Text(
                                    if (c.ws) "CDN 线路 · WebSocket" else "直连线路 · 443",
                                    fontSize = 11.sp, color = C_SUB
                                )
                            }
                            Text(
                                "✎", fontSize = 15.sp, color = C_PRI,
                                modifier = Modifier
                                    .clickable { openEditor(c) }
                                    .padding(4.dp)
                            )
                            Text(
                                "🗑", fontSize = 15.sp, color = C_DANGER,
                                modifier = Modifier
                                    .clickable { delTarget = c }
                                    .padding(4.dp)
                            )
                        }
                    }
                }
            }

            Spacer(Modifier.height(8.dp))

            // 代理域名白名单(核心分流设置)
            Surface(
                shape = RoundedCornerShape(14.dp),
                color = C_CARD,
                shadowElevation = 2.dp,
                modifier = Modifier.fillMaxWidth()
            ) {
                Column(Modifier.padding(12.dp)) {
                    Row(verticalAlignment = Alignment.CenterVertically) {
                        Text("代理域名", fontSize = 13.sp, fontWeight = FontWeight.Bold, color = C_TEXT)
                        Spacer(Modifier.width(6.dp))
                        Text("一行一个,仅这些域名走隧道", fontSize = 10.sp, color = C_SUB)
                        Spacer(Modifier.weight(1f))
                        Text(
                            "保存", fontSize = 12.sp, color = C_PRI,
                            modifier = Modifier.clickable { saveDomains() }
                        )
                    }
                    Spacer(Modifier.height(6.dp))
                    OutlinedTextField(
                        value = domainsInput.value,
                        onValueChange = { domainsInput.value = it },
                        placeholder = { Text("github.com\ngoogle.com\nyoutube.com") },
                        modifier = Modifier.fillMaxWidth().height(120.dp)
                    )
                    Text(
                        "白名单完整替换默认列表;清空保存 = 恢复默认 8 条;改完断开重连生效",
                        fontSize = 10.sp, color = C_SUB,
                        modifier = Modifier.padding(top = 4.dp)
                    )
                }
            }

            Spacer(Modifier.height(8.dp))

            // 直连 DNS 设置(仅非白名单域名用它解析)
            Surface(
                shape = RoundedCornerShape(14.dp),
                color = C_CARD,
                shadowElevation = 2.dp,
                modifier = Modifier.fillMaxWidth()
            ) {
                Column(Modifier.padding(12.dp)) {
                    Row(verticalAlignment = Alignment.CenterVertically) {
                        Text("直连 DNS", fontSize = 13.sp, fontWeight = FontWeight.Bold, color = C_TEXT)
                        Spacer(Modifier.width(6.dp))
                        Text("多个逗号分隔,按序尝试", fontSize = 10.sp, color = C_SUB)
                        Spacer(Modifier.weight(1f))
                        Text(
                            "保存", fontSize = 12.sp, color = C_PRI,
                            modifier = Modifier.clickable {
                                if (dnsInput.value.isNotBlank()) saveDns()
                            }
                        )
                    }
                    Spacer(Modifier.height(6.dp))
                    OutlinedTextField(
                        value = dnsInput.value,
                        onValueChange = { dnsInput.value = it },
                        placeholder = { Text("223.5.5.5") },
                        singleLine = true,
                        modifier = Modifier.fillMaxWidth()
                    )
                    Text(
                        "白名单之外域名的解析上游,国内公共 DNS 即可;改完断开重连生效",
                        fontSize = 10.sp, color = C_SUB,
                        modifier = Modifier.padding(top = 4.dp)
                    )
                }
            }

            // 编辑对话框
            if (editorShow.value) {
                EditorDialog()
            }

            // 删除确认
            delTarget?.let { target ->
                AlertDialog(
                    onDismissRequest = { delTarget = null },
                    title = { Text("删除服务器") },
                    text = { Text("确定删除 ${target.server} ?") },
                    confirmButton = {
                        Text(
                            "删除", color = C_DANGER,
                            modifier = Modifier.clickable {
                                ConfigStore.remove(this@MainActivity, target)
                                configs.value = ConfigStore.load(this@MainActivity)
                                if (active.intValue >= configs.value.size) {
                                    active.intValue = configs.value.size - 1
                                }
                                if (running.value) {
                                    TunService.stopAll(this@MainActivity)
                                    running.value = false
                                    status.value = "未连接"
                                }
                                delTarget = null
                            }
                        )
                    },
                    dismissButton = {
                        Text("取消", modifier = Modifier.clickable { delTarget = null })
                    }
                )
            }

            Spacer(Modifier.height(12.dp))
            Text(
                "选中的配置为当前线路 · 切换或修改后自动重连",
                fontSize = 11.sp, color = C_SUB,
                modifier = Modifier.padding(bottom = 20.dp)
            )
        }
    }

    // ---------- 编辑对话框 ----------

    @Composable
    private fun EditorDialog() {
        AlertDialog(
            onDismissRequest = { closeEditor() },
            title = { Text(if (editorIsNew) "添加服务器" else "编辑服务器") },
            text = {
                Column(verticalArrangement = Arrangement.spacedBy(10.dp)) {
                    OutlinedTextField(
                        value = editorServer.value,
                        onValueChange = { editorServer.value = it },
                        label = { Text("服务器地址 域名:端口") },
                        singleLine = true,
                        modifier = Modifier.fillMaxWidth()
                    )
                    OutlinedTextField(
                        value = editorPass.value,
                        onValueChange = { editorPass.value = it },
                        label = { Text("认证密码") },
                        singleLine = true,
                        modifier = Modifier.fillMaxWidth()
                    )
                    Row(verticalAlignment = Alignment.CenterVertically) {
                        Text("CDN 线路(WebSocket)", Modifier.weight(1f), fontSize = 13.sp)
                        Switch(checked = editorWs.value, onCheckedChange = { editorWs.value = it })
                    }
                    if (editorHint.value.isNotEmpty()) {
                        Text(editorHint.value, fontSize = 12.sp, color = C_DANGER)
                    }
                }
            },
            confirmButton = {
                Text(
                    "保存", color = C_PRI, fontSize = 14.sp,
                    modifier = Modifier.clickable { saveEditor() }
                )
            },
            dismissButton = {
                Text(
                    "取消", color = C_SUB, fontSize = 14.sp,
                    modifier = Modifier.clickable { closeEditor() }
                )
            }
        )
    }
}
