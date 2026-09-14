import { Want } from '@kit.AbilityKit';
import { vpnExtension, connection } from '@kit.NetworkKit';
import { scanBarcode, scanCore } from '@kit.ScanKit';
import { preferences } from '@kit.ArkData';
import { fileIo } from '@kit.CoreFileKit';
import { hilog } from '@kit.PerformanceAnalysisKit';
import { util } from '@kit.ArkTS';
import tun from 'libentry.so';
import { commonEventManager } from '@kit.BasicServicesKit';
import { router } from '@kit.ArkUI';

const TAG = 'CaotunUI';
const CFG_NAME = 'caotun_cfg';
const VPN_WANT: Want = { deviceId: '', bundleName: 'com.caotun.app', abilityName: 'VpnAbility' };

// 配色
const C_BG = '#F3F5F9';
const C_CARD = '#FFFFFF';
const C_PRI = '#3B82F6';
const C_OK = '#22C55E';
const C_DANGER = '#EF4444';
const C_TEXT = '#1B2230';
const C_SUB = '#8A93A6';
const C_LINE = '#E8ECF2';

class Cfg {
  server: string = '';
  pass: string = '';
  ws: boolean = false;
}

@Entry
@Component
struct Index {
  @State configs: Cfg[] = [];
  @State active: number = -1;
  @State status: string = '未接入';
  @State running: boolean = false;
  @State lastErr: string = '';
  private lastStart: number = 0;
  @State dnsInput: string = '';
  private transitionUntil: number = 0; // 启停过渡期:此间禁止心跳刷新抢状态
  private statusSub?: commonEventManager.CommonEventSubscriber;

  aboutToAppear(): void {
    this.loadAll();
    this.subscribeStatus();
    setInterval(() => this.refresh(), 3000);
  }

  // ---------- 配置存取(扁平键 n / c{i}.server / c{i}.pass / c{i}.ws) ----------

  private async loadAll(): Promise<void> {
    const store = await preferences.getPreferences(getContext(this), CFG_NAME);
    const n = await store.get('n', 0) as number;
    const list: Cfg[] = [];
    for (let i = 0; i < n; i++) {
      const c = new Cfg();
      c.server = await store.get(`c${i}.server`, '') as string;
      c.pass = await store.get(`c${i}.pass`, '') as string;
      c.ws = await store.get(`c${i}.ws`, false) as boolean;
      if (c.server) {
        list.push(c);
      }
    }
    // 旧版单配置迁移
    if (list.length === 0) {
      const legacy = await store.get('server', '') as string;
      if (legacy) {
        const c = new Cfg();
        c.server = legacy;
        c.pass = await store.get('pass', '') as string;
        c.ws = await store.get('ws', false) as boolean;
        list.push(c);
        await this.persistAll(store, list);
        await store.put('n', 1);
        await store.flush();
      }
    }
    this.configs = list;
    this.active = list.length > 0 ? 0 : -1;
    if (this.active >= 0) {
      await this.mirrorActive(store, 0);
    }
    this.dnsInput = await store.get('dns', '223.5.5.5') as string;
    if (this.beatFresh()) {
      this.running = true;
      this.status = '已连接';
    } else if (this.active >= 0 && await store.get('autostart', true) as boolean) {
      this.startVpn();
    } else {
      this.status = list.length > 0 ? '未连接' : '扫码添加你的第一台服务器';
    }
  }

  private async persistAll(store: preferences.Preferences, list: Cfg[]): Promise<void> {
    await store.put('n', list.length);
    for (let i = 0; i < list.length; i++) {
      await store.put(`c${i}.server`, list[i].server);
      await store.put(`c${i}.pass`, list[i].pass);
      await store.put(`c${i}.ws`, list[i].ws);
    }
    await store.flush();
  }

  private async mirrorActive(store: preferences.Preferences, i: number): Promise<void> {
    if (i < 0 || i >= this.configs.length) {
      return;
    }
    await store.put('server', this.configs[i].server);
    await store.put('pass', this.configs[i].pass);
    await store.put('ws', this.configs[i].ws);
    await store.put('dialip', '');
    await store.flush();
  }

  // ---------- 心跳与状态 ----------

  private beatFresh(): boolean {
    try {
      const path = getContext(this).filesDir + '/beat';
      if (!fileIo.accessSync(path)) {
        return false;
      }
      const f = fileIo.openSync(path, fileIo.OpenMode.READ_ONLY);
      const stat = fileIo.statSync(f.fd);
      const buf = new ArrayBuffer(stat.size);
      fileIo.readSync(f.fd, buf);
      fileIo.closeSync(f);
      const beat = parseInt(util.TextDecoder.create('utf-8').decodeToString(new Uint8Array(buf)));
      return beat > 0 && (Date.now() - beat) < 15000;
    } catch (e) {
      return false;
    }
  }

  /** 保存 DNS 上游设置(多个逗号分隔);重连后生效 */
  private async saveDns(): Promise<void> {
    const v = this.dnsInput.trim();
    if (!v) {
      return;
    }
    const store = await preferences.getPreferences(getContext(this), CFG_NAME);
    await store.put('dns', v);
    await store.flush();
    this.lastErr = this.running ? 'DNS 已保存,断开重连后生效' : 'DNS 已保存';
  }

  private async refresh(): Promise<void> {
    if (Date.now() < this.transitionUntil) {
      return; // 启停过渡期,乐观状态优先
    }
    if (this.status === '连接中...' && Date.now() - this.lastStart < 20000) {
      return;
    }
    const fresh = this.beatFresh();
    if (fresh !== this.running) {
      this.running = fresh;
      this.status = fresh ? '已连接' : '未连接';
    }
    if (!fresh && this.active >= 0 && Date.now() - this.lastStart > 20000) {
      const store = await preferences.getPreferences(getContext(this), CFG_NAME);
      if (await store.get('autostart', true) as boolean) {
        this.startVpn();
      }
    }
  }

  private subscribeStatus(): void {
    const that = this;
    commonEventManager.createSubscriber({ events: ['caotun_status'] }, (err, sub) => {
      if (err || !sub) {
        return;
      }
      that.statusSub = sub;
      commonEventManager.subscribe(sub, (e, data) => {
        if (e || !data) {
          return;
        }
        that.running = data.code === 1;
        that.status = that.running ? '已连接' : '未连接';
      });
    });
  }

  // ---------- VPN 启停 ----------

  private toggleVpn(): void {
    if (this.running) {
      this.stopVpn();
    } else {
      this.startVpn();
    }
  }

  private async startVpn(): Promise<void> {
    if (this.active < 0 || this.active >= this.configs.length) {
      this.lastErr = '请先扫码或添加服务器';
      return;
    }
    hilog.info(0x0000, TAG, 'startVpn called');
    this.transitionUntil = Date.now() + 8000;
    this.running = true; // 点击即响应
    this.status = '连接中...';
    try {
      const store = await preferences.getPreferences(getContext(this), CFG_NAME);
      await store.put('autostart', true);
      try {
        const host = this.configs[this.active].server.split(':')[0];
        const addrs = await connection.getAddressesByName(host);
        for (const a of addrs) {
          if (a.address.indexOf(':') < 0) {
            await store.put('dialip', a.address);
            break;
          }
        }
      } catch (e) {
        hilog.error(0x0000, TAG, 'resolve failed: %{public}s', JSON.stringify(e));
      }
      await store.flush();
      this.lastStart = Date.now();
      await vpnExtension.startVpnExtensionAbility(VPN_WANT);
    } catch (e) {
      hilog.error(0x0000, TAG, 'startVpn failed: %{public}s', JSON.stringify(e));
      this.running = this.beatFresh();
      this.lastErr = '启动失败';
      this.status = this.running ? '已连接' : '启动失败';
    }
  }

  private async stopVpn(): Promise<void> {
    this.transitionUntil = Date.now() + 8000;
    this.running = false; // 点击即响应
    this.status = '未连接';
    try {
      const store = await preferences.getPreferences(getContext(this), CFG_NAME);
      await store.put('autostart', false);
      await store.flush();
      await vpnExtension.stopVpnExtensionAbility(VPN_WANT);
    } catch (e) {
      this.lastErr = '停止失败';
    }
  }

  // ---------- 配置管理 ----------

  private openEditor(i: number): void {
    AppStorage.setOrCreate('editIdx', i);
    AppStorage.setOrCreate('editSaved', false);
    if (i >= 0 && i < this.configs.length) {
      AppStorage.setOrCreate('editServer', this.configs[i].server);
      AppStorage.setOrCreate('editPass', this.configs[i].pass);
      AppStorage.setOrCreate('editWs', this.configs[i].ws);
    } else {
      AppStorage.setOrCreate('editServer', '');
      AppStorage.setOrCreate('editPass', '');
      AppStorage.setOrCreate('editWs', false);
    }
    router.pushUrl({ url: 'pages/Edit' });
  }

  /** 从编辑页返回时应用结果 */
  onPageShow(): void {
    if (!AppStorage.get<boolean>('editSaved')) {
      return;
    }
    AppStorage.setOrCreate('editSaved', false);
    const c = new Cfg();
    c.server = (AppStorage.get<string>('editServer') ?? '').trim();
    c.pass = (AppStorage.get<string>('editPass') ?? '').trim();
    c.ws = AppStorage.get<boolean>('editWs') ?? false;
    const idx = AppStorage.get<number>('editIdx') ?? -1;
    if (!c.server || !c.pass) {
      return;
    }
    const restartNow = idx >= 0 && idx === this.active && this.running;
    if (idx < 0) {
      this.configs.push(c);
      this.active = this.configs.length - 1;
    } else if (idx < this.configs.length) {
      this.configs[idx] = c;
    }
    preferences.getPreferences(getContext(this), CFG_NAME).then((store) => {
      this.persistAll(store, this.configs).then(() => {
        if (this.active >= 0) {
          this.mirrorActive(store, this.active);
        }
        if (restartNow) {
          this.stopVpn().then(() => {
            this.startVpn();
          });
        }
      });
    });
  }

  /** 删除需二次确认,防误触 */
  private delCfg(i: number): void {
    AlertDialog.show({
      message: '确定删除 ' + this.configs[i].server + ' ?',
      primaryButton: {
        value: '取消',
        action: () => {
        }
      },
      secondaryButton: {
        value: '删除',
        fontColor: '#FFFFFF',
        backgroundColor: '#EF4444',
        action: () => {
          this.doDelCfg(i);
        }
      }
    });
  }

  private async doDelCfg(i: number): Promise<void> {
    const wasActive = i === this.active;
    this.configs.splice(i, 1);
    const store = await preferences.getPreferences(getContext(this), CFG_NAME);
    await this.persistAll(store, this.configs);
    if (wasActive) {
      await this.stopVpn();
      this.active = this.configs.length > 0 ? 0 : -1;
      if (this.active >= 0) {
        await this.mirrorActive(store, this.active);
      } else {
        await store.put('server', '');
        await store.flush();
        this.status = '扫码添加你的第一台服务器';
      }
    } else if (i < this.active) {
      this.active--;
    }
  }

  private async selectCfg(i: number): Promise<void> {
    if (i === this.active) {
      return;
    }
    this.active = i;
    const store = await preferences.getPreferences(getContext(this), CFG_NAME);
    await this.mirrorActive(store, i);
    await store.put('autostart', true);
    await store.flush();
    if (this.running) {
      await this.stopVpn();
    }
    this.startVpn();
  }

  private async scanAndStart(): Promise<void> {
    try {
      const opt: scanBarcode.ScanOptions = { scanTypes: [scanCore.ScanType.QR_CODE] };
      const r: scanBarcode.ScanResult = await scanBarcode.startScanForResult(getContext(this), opt);
      const uri = r.originalValue;
      if (!uri || !uri.startsWith('caotun://')) {
        this.lastErr = '不是有效的 caotun 二维码';
        return;
      }
      const rest = uri.slice('caotun://'.length);
      const qi = rest.indexOf('?');
      const server = qi >= 0 ? rest.slice(0, qi) : rest;
      let pass = '';
      let ws = false;
      let dns = '';
      if (qi >= 0) {
        for (const kv of rest.slice(qi + 1).split('&')) {
          const i = kv.indexOf('=');
          if (i < 0) {
            continue;
          }
          const k = kv.slice(0, i);
          const v = kv.slice(i + 1);
          if (k === 'p') {
            pass = decodeURIComponent(v);
          } else if (k === 'ws') {
            ws = v === '1';
          } else if (k === 'dns') {
            dns = decodeURIComponent(v);
          }
        }
      }
      if (!server || !pass) {
        this.lastErr = '二维码缺少地址或密码';
        return;
      }
      const store = await preferences.getPreferences(getContext(this), CFG_NAME);
      let idx = -1;
      for (let i = 0; i < this.configs.length; i++) {
        if (this.configs[i].server === server) {
          idx = i;
          break;
        }
      }
      const c = new Cfg();
      c.server = server;
      c.pass = pass;
      c.ws = ws;
      if (idx >= 0) {
        this.configs[idx] = c;
      } else {
        this.configs.push(c);
        idx = this.configs.length - 1;
      }
      await this.persistAll(store, this.configs);
      this.active = idx;
      await this.mirrorActive(store, idx);
      if (dns.trim() !== '') {
        await store.put('dns', dns.trim()); // 扫码导入的 DNS 上游写入既有偏好，断开重连后生效
      }
      await store.put('autostart', true);
      await store.flush();
      this.lastErr = '';
      if (this.running) {
        await this.stopVpn();
      }
      this.startVpn();
    } catch (e) {
      hilog.error(0x0000, TAG, 'scan failed: %{public}s', JSON.stringify(e));
      this.lastErr = '扫码失败';
    }
  }

  build() {
    Stack({ alignContent: Alignment.TopStart }) {
      // 顶部品牌色块背景
      Column()
        .width('100%').height(230)
        .linearGradient({ angle: 160, colors: [['#3B82F6', 0], ['#60A5FA', 1]] })

      Scroll() {
        Column({ space: 14 }) {
          // 顶栏
          Row() {
            Text('caotun').fontSize(22).fontWeight(FontWeight.Bold).fontColor('#FFFFFF')
            Blank()
            Row({ space: 6 }) {
              Circle().width(8).height(8)
                .fill(this.running ? '#BBF7D0' : '#FDE68A')
              Text(this.running ? '已连接' : '未连接')
                .fontSize(12).fontColor('#FFFFFF')
            }
            .padding({ left: 10, right: 10, top: 5, bottom: 5 })
            .borderRadius(20)
            .backgroundColor('rgba(255,255,255,0.18)')
          }.width('100%').margin({ top: 8 })

          // 仪表卡:大圆钮
          Column({ space: 10 }) {
            // 大圆连接钮
            Stack() {
              Circle()
                .width(116).height(116)
                .fill(this.running ? '#22C55E' : Color.White)
                .stroke(this.running ? '#16A34A' : C_PRI)
                .strokeWidth(4)
              Column({ space: 4 }) {
                Text(this.running ? '⏻' : '⏻')
                  .fontSize(30)
                  .fontColor(this.running ? '#FFFFFF' : C_PRI)
                Text(this.running ? '断开' : '连接')
                  .fontSize(13)
                  .fontColor(this.running ? '#FFFFFF' : C_PRI)
              }
            }
            .onClick(() => this.toggleVpn())

            // 当前线路
            Column({ space: 2 }) {
              Text(this.active >= 0 && this.active < this.configs.length ?
                this.configs[this.active].server : '未选择服务器')
                .fontSize(15).fontWeight(FontWeight.Medium).fontColor(C_TEXT)
                .maxLines(1).textOverflow({ overflow: TextOverflow.Ellipsis })
              Text(this.running ? '全局代理 · 加密隧道中' : '点击圆钮连接')
                .fontSize(12).fontColor(C_SUB)
            }
          }
          .width('100%')
          .padding({ top: 22, bottom: 22 })
          .borderRadius(20)
          .backgroundColor(C_CARD)
          .shadow({ radius: 12, color: 'rgba(30,60,120,0.10)', offsetY: 4 })

          // 错误条
          if (this.lastErr) {
            Row({ space: 6 }) {
              Text('!').fontSize(11).fontColor('#FFFFFF')
                .width(16).height(16).textAlign(TextAlign.Center)
                .borderRadius(8).backgroundColor(C_DANGER)
              Text(this.lastErr).fontSize(12).fontColor(C_DANGER)
                .layoutWeight(1).maxLines(2)
            }
            .width('100%').padding(10)
            .borderRadius(12).backgroundColor('#FEF2F2')
          }

          // 服务器列表标题
          Row() {
            Text('服务器').fontSize(15).fontWeight(FontWeight.Bold).fontColor(C_TEXT)
            Blank()
            Text('＋ 扫码').fontSize(13).fontColor(C_PRI).onClick(() => this.scanAndStart())
            Text('｜').fontSize(12).fontColor(C_LINE)
            Text('＋ 手动').fontSize(13).fontColor(C_PRI).onClick(() => this.openEditor(-1))
          }.width('100%')

          // 服务器卡片列表
          List({ space: 8 }) {
            ForEach(this.configs, (c: Cfg, i: number) => {
              ListItem() {
                Row({ space: 10 }) {
                  // 选中圆点
                  Circle()
                    .width(18).height(18)
                    .fill(i === this.active ? C_PRI : Color.Transparent)
                    .stroke(i === this.active ? C_PRI : '#C6CCD8')
                    .strokeWidth(2)
                  Column({ space: 2 }) {
                    Text(c.server).fontSize(15).fontWeight(FontWeight.Medium).fontColor(C_TEXT)
                      .maxLines(1).textOverflow({ overflow: TextOverflow.Ellipsis })
                    Text(c.ws ? 'CDN 线路 · WebSocket' : '直连线路 · 443')
                      .fontSize(11).fontColor(C_SUB)
                  }.alignItems(HorizontalAlign.Start).layoutWeight(1)

                  Text('✎').fontSize(15)
                    .fontColor(this.running && i === this.active ? '#C6CCD8' : C_PRI)
                    .padding(4)
                    .onClick(() => {
                      if (this.running && i === this.active) {
                        this.lastErr = '请先断开,再编辑当前线路';
                        return;
                      }
                      this.openEditor(i);
                    })
                  Text('🗑').fontSize(15).fontColor(C_DANGER)
                    .padding(4)
                    .onClick(() => this.delCfg(i))
                }
                .width('100%')
                .padding(12)
                .borderRadius(14)
                .backgroundColor(C_CARD)
                .shadow({ radius: 6, color: 'rgba(30,60,120,0.06)', offsetY: 2 })
                .onClick(() => this.selectCfg(i))
              }
            }, (c: Cfg, i: number) => `${i}-${c.server}`)
          }
          .width('100%')
          .height(this.configs.length > 3 ? 190 : this.configs.length * 66)

          // 空态
          if (this.configs.length === 0) {
            Column({ space: 6 }) {
              Text('🌐').fontSize(34)
              Text('还没有服务器').fontSize(14).fontColor(C_SUB)
            }.width('100%').padding(24).borderRadius(14).backgroundColor(C_CARD)
          }

          // DNS 设置
          Column({ space: 6 }) {
            Row({ space: 6 }) {
              Text('DNS').fontSize(13).fontWeight(FontWeight.Bold).fontColor(C_TEXT)
              Text('多个逗号分隔,按序尝试').fontSize(10).fontColor(C_SUB)
              Blank()
              Text('保存').fontSize(12).fontColor(C_PRI)
                .onClick(() => this.saveDns())
            }.width('100%')
            TextInput({ text: this.dnsInput, placeholder: '223.5.5.5' })
              .height(38).borderRadius(8)
              .onChange((v: string) => this.dnsInput = v)
            Text('上游解析器:国内直连探测与隧道解析共用;改完断开重连生效')
              .fontSize(10).fontColor(C_SUB)
          }
          .width('100%')
          .padding(12)
          .borderRadius(14)
          .backgroundColor(C_CARD)
          .shadow({ radius: 6, color: 'rgba(30,60,120,0.06)', offsetY: 2 })

          Text('选中的配置为当前线路 · 切换或修改后自动重连')
            .fontSize(11).fontColor(C_SUB)
            .margin({ top: 2, bottom: 20 })
        }
        .width('100%')
        .padding({ left: 16, right: 16, top: 10, bottom: 10 })
      }
      .width('100%').height('100%')
      .scrollBar(BarState.Off)
      .align(Alignment.TopStart)
      .edgeEffect(EdgeEffect.None)
    }
    .width('100%').height('100%')
    .backgroundColor(C_BG)
    .gesture(TapGesture().onAction(() => this.getUIContext().getFocusController().clearFocus()))
  }
}
