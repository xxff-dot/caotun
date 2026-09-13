import { Want } from '@kit.AbilityKit';
import { VpnExtensionAbility, vpnExtension, connection } from '@kit.NetworkKit';
import { commonEventManager } from '@kit.BasicServicesKit';
import { hilog } from '@kit.PerformanceAnalysisKit';
import { preferences } from '@kit.ArkData';
import { socket } from '@kit.NetworkKit';
import { fileIo } from '@kit.CoreFileKit';
import { util } from '@kit.ArkTS';
import tun from 'libentry.so';

const TAG = 'CaotunVpn';
const CFG_NAME = 'caotun_cfg';
const TUN_ADDR = '10.111.0.1';
const TUN_PREFIX = 30;
const TUN_MTU = 1400;

interface CaotunConfig {
  server: string;
  pass: string;
  ws: boolean;
  dialIP: string;
  dns: string;
}

/**
 * VPN 扩展：系统授权后在此创建 tun 虚拟网卡，
 * 把 fd 交给 Go 引擎（libcaotun.so）做 tun2sock。
 */
export default class VpnAbility extends VpnExtensionAbility {
  private conn?: vpnExtension.VpnConnection;
  private beatTimer: number = -1;

  onCreate(want: Want): void {
    hilog.info(0x0000, TAG, '%{public}s', 'vpn onCreate');
    this.setup();
  }

  private async setup(): Promise<void> {
    try {
      const cfg = await this.loadCfg();
      if (!cfg) {
        hilog.error(0x0000, TAG, '%{public}s', '无配置，请先在主界面扫码');
        return;
      }
      const conn = vpnExtension.createVpnConnection(this.context);
      this.conn = conn;
      // create 返回 tun 网卡的 fd；isBlocking=true 保证 Go 侧阻塞读语义
      // ClashBox 生产配方:/0 单条路由(网关=tun 自身地址)+ DNS 哨兵 + 自身豁免防回环
      const fd = await conn.create({
        addresses: [{ address: { address: TUN_ADDR, family: 1 }, prefixLength: TUN_PREFIX }],
        routes: [
          {
            interface: 'vpn-tun',
            destination: { address: { address: '0.0.0.0', family: 1 }, prefixLength: 0 },
            gateway: { address: TUN_ADDR, family: 1 },
            hasGateway: false,
            isDefaultRoute: true
          }
        ],
        dnsAddresses: ['10.111.0.2'], // DNS 哨兵:tun /30 网段内的对端地址,系统解析器查询会经子网路由进 tun,由引擎劫持
        mtu: TUN_MTU,
        isIPv4Accepted: true,
        isIPv6Accepted: false,
        isBlocking: true
      } as vpnExtension.VpnConfig);
      hilog.info(0x0000, TAG, 'tun fd=%{public}d', fd);
      const dir = this.context.cacheDir + '/caotun';
      // CN 分流段表:从 rawfile 拷到缓存目录供引擎加载
      try {
        const raw = this.context.resourceManager.getRawFileContentSync('cn_cidr.txt');
        const cf = fileIo.openSync(dir + '/cn_cidr.txt', fileIo.OpenMode.READ_WRITE | fileIo.OpenMode.CREATE | fileIo.OpenMode.TRUNC);
        fileIo.writeSync(cf.fd, raw.buffer);
        fileIo.closeSync(cf);
      } catch (e) {
        hilog.error(0x0000, TAG, 'copy cn_cidr failed: %{public}s', JSON.stringify(e));
      }
      // VPN 启动后系统 DNS 走 tun 会与引擎自解析形成死锁,先把服务器 IP 解析好落盘给 Go 用
      try {
        const host = cfg.server.split(':')[0];
        const addrs = await connection.getAddressesByName(host);
        for (const a of addrs) {
          if (a.address.indexOf(':') < 0) {
            const f = fileIo.openSync(dir + '/dial.ip', fileIo.OpenMode.READ_WRITE | fileIo.OpenMode.CREATE | fileIo.OpenMode.TRUNC);
            fileIo.writeSync(f.fd, a.address);
            fileIo.closeSync(f);
            hilog.info(0x0000, TAG, 'dial.ip=%{public}s', a.address);
            break;
          }
        }
      } catch (e) {
        hilog.error(0x0000, TAG, 'resolve failed: %{public}s', JSON.stringify(e));
      }
      const protectPath = this.context.filesDir + '/protect.sock';
      this.startProtectServer(conn, protectPath);
      const code = tun.startTun(cfg.server, cfg.pass, dir, fd, TUN_MTU, cfg.ws ? 1 : 0, protectPath, cfg.dialIP, dir + '/cn_cidr.txt', cfg.dns);
      hilog.info(0x0000, TAG, 'startTun code=%{public}d', code);
      // 心跳状态写入 preferences:VPN 跑在独立进程,UI 进程读不到本进程的 Go 实例
      const store = await preferences.getPreferences(this.context, CFG_NAME);
      await store.put('running', code === 0);
      await store.put('beat', Date.now());
      await store.flush();
      this.publishStatus(code === 0);
      // 心跳:每 5 秒刷新心跳文件;进程被强杀时心跳过期,UI 据此判定真实状态
      this.beatTimer = setInterval(() => this.writeBeat(), 5000);
      this.writeBeat();
    } catch (e) {
      hilog.error(0x0000, TAG, 'setup failed: %{public}s', JSON.stringify(e));
    }
  }

  /** 状态广播:preferences 不跨进程同步,UI 靠 commonEvent 感知运行状态 */
  private async publishStatus(running: boolean): Promise<void> {
    commonEventManager.publish('caotun_status', { code: running ? 1 : 0 }, (err) => {
      if (err) {
        hilog.error(0x0000, TAG, 'publish failed: %{public}s', JSON.stringify(err));
      }
    });
  }

/**
 * protect 通道:引擎拨号前把 fd 号发到这个 unix socket,
 * 这里调 VpnConnection.protect(fd) 打免捕获标记后回 ok
 */
private startProtectServer(vpnConn: vpnExtension.VpnConnection, path: string): void {
  try {
    if (fileIo.accessSync(path)) {
      fileIo.unlinkSync(path); // 清理上次进程残留,避免 bind 失败
    }
  } catch (e) {
  }
  const server = socket.constructLocalSocketServerInstance();
  server.listen({ address: path }).then(() => {
    hilog.info(0x0000, TAG, 'protect server on %{public}s', path);
    server.on('connect', (client: socket.LocalSocketConnection) => {
      client.on('message', (value: socket.LocalSocketMessageInfo) => {
        const fdStr = new util.TextDecoder().decodeToString(new Uint8Array(value.message));
        const fd = parseInt(fdStr);
        if (Number.isNaN(fd)) {
          client.send({ data: 'err' });
          return;
        }
        vpnConn.protect(fd).then(() => {
          client.send({ data: 'ok' });
        }).catch((e: Error) => {
          hilog.error(0x0000, TAG, 'protect fd=%{public}d failed: %{public}s', fd, JSON.stringify(e));
          client.send({ data: 'err' });
        });
      });
    });
  }).catch((e: Error) => {
    hilog.error(0x0000, TAG, 'protect listen failed: %{public}s', JSON.stringify(e));
  });
}

  private beatPath(): string {
    return this.context.filesDir + '/beat';
  }

  private writeBeat(): void {
    try {
      const f = fileIo.openSync(this.beatPath(), fileIo.OpenMode.READ_WRITE | fileIo.OpenMode.CREATE | fileIo.OpenMode.TRUNC);
      fileIo.writeSync(f.fd, Date.now().toString());
      fileIo.closeSync(f);
    } catch (e) {
      hilog.error(0x0000, TAG, 'writeBeat failed: %{public}s', JSON.stringify(e));
    }
  }

  private async loadCfg(): Promise<CaotunConfig | null> {
    const store = await preferences.getPreferences(this.context, CFG_NAME);
    const server = await store.get('server', '') as string;
    const pass = await store.get('pass', '') as string;
    if (!server || !pass) {
      return null;
    }
    const ws = await store.get('ws', false) as boolean;
    const dialIP = await store.get('dialip', '') as string;
    const dns = await store.get('dns', '223.5.5.5') as string;
    return { server, pass, ws, dialIP, dns };
  }

  async onDestroy(): Promise<void> {
    tun.stopTun();
    if (this.beatTimer !== -1) {
      clearInterval(this.beatTimer);
      this.beatTimer = -1;
    }
    if (this.conn) {
      this.conn.destroy();
    }
    const store = await preferences.getPreferences(this.context, CFG_NAME);
    await store.put('running', false);
    await store.flush();
    try {
      if (fileIo.accessSync(this.beatPath())) {
        fileIo.unlinkSync(this.beatPath());
      }
    } catch (e) {
    }
    this.publishStatus(false);
    hilog.info(0x0000, TAG, '%{public}s', 'vpn onDestroy');
  }
}
