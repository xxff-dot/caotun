/**
 * libcaotun.so(Go 引擎)的 NAPI 绑定。
 * 返回码: 0=成功启动, 1=已在运行, 2=参数无效
 */
export const startTun: (server: string, pass: string, dir: string,
  fd: number, mtu: number, ws: number, protectPath: string, dialIP: string, cnPath: string) => number;
export const stopTun: () => void;
export const running: () => number;
export const lastError: () => string;
