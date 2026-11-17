// Package sys 封装 Windows 系统相关功能：单实例、窗口钩子、网卡地址等。
package sys

import (
	"net"
	"sort"
	"strings"
)

// LocalIP 是一块网卡上的 IPv4 地址。
type LocalIP struct {
	IP        string
	Interface string
	Virtual   bool // VMware、Hyper-V、WSL、ZeroTier 等虚拟网卡
}

// LocalIPv4s 返回本机所有已启用、非回环网卡上的 IPv4 地址（私有地址排在前面）。
func LocalIPv4s() []LocalIP {
	var out []LocalIP
	ifs, err := net.Interfaces()
	if err != nil {
		return nil
	}
	for _, it := range ifs {
		if it.Flags&net.FlagUp == 0 || it.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, _ := it.Addrs()
		for _, a := range addrs {
			ipn, ok := a.(*net.IPNet)
			if !ok {
				continue
			}
			v4 := ipn.IP.To4()
			if v4 == nil || v4.IsLinkLocalUnicast() {
				continue
			}
			out = append(out, LocalIP{IP: v4.String(), Interface: it.Name, Virtual: isVirtual(it.Name)})
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		pi, pj := net.ParseIP(out[i].IP).IsPrivate(), net.ParseIP(out[j].IP).IsPrivate()
		if pi != pj {
			return pi
		}
		// 虚拟网卡往后排
		return !isVirtual(out[i].Interface) && isVirtual(out[j].Interface)
	})
	return out
}

func isVirtual(name string) bool {
	n := strings.ToLower(name)
	for _, k := range []string{"vmware", "virtualbox", "vethernet", "hyper-v", "wsl", "docker", "zerotier", "tailscale", "loopback"} {
		if strings.Contains(n, k) {
			return true
		}
	}
	return false
}
