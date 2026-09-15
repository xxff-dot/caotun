// 哨兵网段守卫:发给 VPN tun 自身 /30 网段(10.111.0.0/30)的非 DNS TCP 流量
// (典型:系统 Private DNS/DoT 853 端口)直连拨号会路由回 tun 形成无限自环,
// 曾把引擎拖死。收到即拒,促系统快速回落普通 DNS。
package tun2sock

import (
	"encoding/binary"
	"net"
)

const sentinelCIDR = "10.111.0.0/30"

func inSentinel(ip net.IP) bool {
	if ip == nil || ip.To4() == nil {
		return false
	}
	u := binary.BigEndian.Uint32(ip.To4())
	_, ipnet, err := net.ParseCIDR(sentinelCIDR)
	if err != nil {
		return false
	}
	base := binary.BigEndian.Uint32(ipnet.IP.To4())
	return u >= base && u <= base+3
}
