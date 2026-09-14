package com.caotun.app

import android.content.Context

/** 单条服务器配置 */
data class Cfg(val server: String, val pass: String, val ws: Boolean)

/** 多配置存储:与鸿蒙端同构(扁平键 n / c{i}.server / c{i}.pass / c{i}.ws) */
object ConfigStore {
    private const val NAME = "caotun_cfg"

    fun load(ctx: Context): List<Cfg> {
        val sp = ctx.getSharedPreferences(NAME, Context.MODE_PRIVATE)
        val n = sp.getInt("n", 0)
        val list = mutableListOf<Cfg>()
        for (i in 0 until n) {
            val s = sp.getString("c$i.server", "") ?: ""
            if (s.isNotEmpty()) {
                list.add(Cfg(s, sp.getString("c$i.pass", "") ?: "", sp.getBoolean("c$i.ws", false)))
            }
        }
        if (list.isEmpty()) {
            sp.getString("server", "")?.takeIf { it.isNotEmpty() }?.let {
                list.add(Cfg(it, sp.getString("pass", "") ?: "", sp.getBoolean("ws", false)))
            }
        }
        return list
    }

    fun saveAll(ctx: Context, list: List<Cfg>) {
        val sp = ctx.getSharedPreferences(NAME, Context.MODE_PRIVATE)
        val e = sp.edit()
        e.putInt("n", list.size)
        for (i in list.indices) {
            e.putString("c$i.server", list[i].server)
            e.putString("c$i.pass", list[i].pass)
            e.putBoolean("c$i.ws", list[i].ws)
        }
        e.apply()
    }

    fun activeCfg(ctx: Context): Cfg? {
        val sp = ctx.getSharedPreferences(NAME, Context.MODE_PRIVATE)
        val server = sp.getString("server", "") ?: ""
        val pass = sp.getString("pass", "") ?: ""
        if (server.isEmpty() || pass.isEmpty()) return null
        return Cfg(server, pass, sp.getBoolean("ws", false))
    }

    fun mirrorActive(ctx: Context, cfg: Cfg) {
        ctx.getSharedPreferences(NAME, Context.MODE_PRIVATE).edit()
            .putString("server", cfg.server)
            .putString("pass", cfg.pass)
            .putBoolean("ws", cfg.ws)
            .putString("dialip", "")
            .apply()
    }

    fun remove(ctx: Context, cfg: Cfg) {
        val list = load(ctx).toMutableList()
        list.removeAll { it.server == cfg.server && it.pass == cfg.pass }
        saveAll(ctx, list)
    }

    fun dns(ctx: Context): String =
        ctx.getSharedPreferences(NAME, Context.MODE_PRIVATE).getString("dns", "223.5.5.5") ?: "223.5.5.5"

    fun saveDns(ctx: Context, v: String) {
        ctx.getSharedPreferences(NAME, Context.MODE_PRIVATE).edit().putString("dns", v).apply()
    }

    fun autostart(ctx: Context): Boolean =
        ctx.getSharedPreferences(NAME, Context.MODE_PRIVATE).getBoolean("autostart", true)

    fun setAutostart(ctx: Context, v: Boolean) {
        ctx.getSharedPreferences(NAME, Context.MODE_PRIVATE).edit().putBoolean("autostart", v).apply()
    }
}
