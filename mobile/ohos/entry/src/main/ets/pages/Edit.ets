import { router } from '@kit.ArkUI';

@Entry
@Component
struct EditPage {
  @State idx: number = -1; // -1 = 新增
  @State server: string = '';
  @State pass: string = '';
  @State ws: boolean = false;
  @State hint: string = '';

  aboutToAppear(): void {
    this.idx = AppStorage.get<number>('editIdx') ?? -1;
    this.server = AppStorage.get<string>('editServer') ?? '';
    this.pass = AppStorage.get<string>('editPass') ?? '';
    this.ws = AppStorage.get<boolean>('editWs') ?? false;
  }

  private save(): void {
    if (this.server.trim() === '' || this.pass.trim() === '') {
      this.hint = '地址与密码必填';
      return;
    }
    AppStorage.setOrCreate('editSaved', true);
    AppStorage.setOrCreate('editIdx', this.idx);
    AppStorage.setOrCreate('editServer', this.server.trim());
    AppStorage.setOrCreate('editPass', this.pass.trim());
    AppStorage.setOrCreate('editWs', this.ws);
    router.back();
  }

  build() {
    Column({ space: 14 }) {
      // 顶部
      Row({ space: 10 }) {
        Text('<').fontSize(20).fontWeight(FontWeight.Bold).fontColor('#1B2230')
          .onClick(() => router.back())
        Text(this.idx < 0 ? '添加服务器' : '编辑服务器')
          .fontSize(18).fontWeight(FontWeight.Bold).fontColor('#1B2230')
      }
      .width('100%')
      .margin({ top: 12 })

      Column({ space: 12 }) {
        Column({ space: 4 }) {
          Text('服务器地址').fontSize(12).fontColor('#8A93A6')
            .alignSelf(ItemAlign.Start)
          TextInput({ text: this.server, placeholder: 'pdl.example.com:8443' })
            .height(46).borderRadius(10)
            .backgroundColor('#F3F5F9')
            .onChange((v: string) => this.server = v)
        }.alignItems(HorizontalAlign.Start).width('100%')

        Column({ space: 4 }) {
          Text('认证密码').fontSize(12).fontColor('#8A93A6')
            .alignSelf(ItemAlign.Start)
          TextInput({ text: this.pass, placeholder: '服务端 auth 密码' })
            .type(InputType.Password)
            .enableAutoFill(false) // 关闭系统"是否保存密码"弹窗
            .height(46).borderRadius(10)
            .backgroundColor('#F3F5F9')
            .onChange((v: string) => this.pass = v)
            .onSubmit(() => this.save())
        }.alignItems(HorizontalAlign.Start).width('100%')

        Row() {
          Column({ space: 2 }) {
            Text('CDN 线路').fontSize(14).fontColor('#1B2230')
            Text('服务器走 WebSocket(橙云域名)').fontSize(10).fontColor('#8A93A6')
          }.alignItems(HorizontalAlign.Start).layoutWeight(1)
          Toggle({ type: ToggleType.Checkbox, isOn: this.ws })
            .onChange((on: boolean) => this.ws = on)
        }
        .width('100%')
        .padding(12)
        .borderRadius(10)
        .backgroundColor('#F3F5F9')

        if (this.hint !== '') {
          Text(this.hint).fontSize(12).fontColor('#EF4444')
        }

        Button('保存')
          .width('100%').height(46)
          .backgroundColor('#3B82F6')
          .onClick(() => this.save())
      }
      .width('100%')
      .padding(16)
      .borderRadius(16)
      .backgroundColor('#FFFFFF')
      .shadow({ radius: 10, color: 'rgba(30,60,120,0.08)', offsetY: 3 })

      Blank()
    }
    .width('100%').height('100%')
    .padding({ left: 16, right: 16, top: 10, bottom: 16 })
    .backgroundColor('#F3F5F9')
    .gesture(TapGesture().onAction(() => this.getUIContext().getFocusController().clearFocus()))
  }
}
