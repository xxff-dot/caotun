# 构建与部署

所有 Go 命令在 `src/` 目录执行。

## 服务端(Linux VPS)

```bash
cd src && GOOS=linux GOARCH=amd64 go build -o ../dist/caotun_linux .   # 或直接 sh scripts/shell/build.sh
scp dist/caotun_linux root@<vps>:/root/caotun/caotun   # 部署到 /root/caotun/,systemd 单元 caotun.service
ssh root@<vps> "chmod +x /root/caotun/caotun && systemctl restart caotun"  # scp 不带执行位,必须 chmod
```

注意:新二进制必须带执行位,否则 systemd 报 `203/EXEC` 无限重启循环。

## 安卓

前置:JDK 17/21(不要用 JDK 25,Kotlin 2.0.20 解析不了)、Android SDK+NDK、`go install golang.org/x/mobile/cmd/gomobile@latest && go install golang.org/x/mobile/cmd/gobind@latest && gomobile init`。

```bash
# 1. 出 AAR(gomobile 要求 go.mod 含 golang.org/x/mobile,需临时升版)
cd src && go get golang.org/x/mobile@latest    # go 指令会被顶到 1.26,构建后还原
gomobile bind -target=android -androidapi 29 -ldflags="-s -w" -o ../mobile/android/app/libs/caotun.aar ./mobile/android
git -C .. checkout -- src/go.mod src/go.sum    # 还原 go.mod(鸿蒙工具链要求 1.24)

# 2. 出 APK(需要 gradle 8.9 + JDK 17/21,如 DevEco JBR)
export JAVA_HOME="C:/Program Files/Huawei/DevEco Studio/jbr"
gradle assembleDebug
# 产物: app/build/outputs/apk/debug/app-debug.apk
```

javac 报 GBK 编码错时:`export JAVA_TOOL_OPTIONS="-Dfile.encoding=UTF-8"`。

只改 Go 引擎不改 Kotlin 时,可以不重打 APK:用 python zipfile 替换 APK 内 `lib/arm64-v8a/libgojni.so`(STORED 压缩)+ zipalign 4 + apksigner(debug.keystore, 密码 android)重签。

## 鸿蒙

前置:DevEco Studio(NDK 随装);ohos-go 工具链一次性准备:`sh scripts/shell/setup-ohos-toolchain.sh`(克隆 openharmony-sig fork 并自举编译到 `tools/ohos-go124`)。

```bash
export OHOS_GO='E:\tools\tcpforward\tools\ohos-go124'   # 必须 Windows 风格路径
sh scripts/shell/build-ohos.sh                           # 产物 mobile/ohos/entry/libs/arm64-v8a/libcaotun.so
# DevEco Studio 打开 mobile/ohos 构建安装;或命令行:
cd mobile/ohos && node "<DevEco>/tools/hvigor/bin/hvigorw.js" --mode module -p product=default assembleHap -p debuggable=true
export DEVECO_SDK_HOME='C:\Program Files\Huawei\DevEco Studio\sdk'   # hvigor 校验此项
hdc -t <target> install -r entry-default-signed.hap
```

坑位:
- `build-ohos.sh` 不要在 `MSYS_NO_PATHCONV=1` 下运行——go.exe 收到 POSIX 路径会把产物写到 `E:\e\...`
- go.mod 必须 `go 1.24.0`(fork 工具链上限),与安卓 AAR 的临时升版互斥,按上面顺序串行
- hvigor 报 `Invalid value of 'DEVECO_SDK_HOME'`:显式 export 一次即可

## 真机验证

- 安卓:`adb shell ip addr | grep 10.111`(tun 存在);引擎日志 `adb shell run-as com.caotun.app cat cache/caotun/engine.log`;`ping google.com` 应回 **198.18.x**(fake-ip 生效标志)
- 鸿蒙:`hdc shell hilog -x --domain 0xC0A0 | grep CaotunEngine`(引擎日志);浏览器实测以页面渲染为准
- 注意:shell 的 `ping` 解析走 shell 自己的解析路径,不代表 App 流量;验证以浏览器 + 引擎日志为准
