package main

import (
	"fmt"
	"net"
	"sort"
)

// The server listens on the LAN as well as the tunnel.
//
// Same wifi means a direct path: 4ms instead of ~78ms and no bandwidth
// ceiling. Access still requires the token, so this isn't an open door.

// primaryIP is the address the OS would use to reach the outside world — i.e.
// the one a phone on the same wifi can actually talk to. No packet is sent; the
// UDP socket just resolves the route.
func primaryIP() string {
	c, err := net.Dial("udp4", "8.8.8.8:80")
	if err != nil {
		return ""
	}
	defer c.Close()
	if ua, ok := c.LocalAddr().(*net.UDPAddr); ok {
		return ua.IP.String()
	}
	return ""
}

// lanIPs returns the machine's private IPv4 addresses, most likely first.
func lanIPs() []string {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil
	}
	var out []string
	for _, ifc := range ifaces {
		if ifc.Flags&net.FlagUp == 0 || ifc.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := ifc.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			ipn, ok := a.(*net.IPNet)
			if !ok {
				continue
			}
			ip := ipn.IP.To4()
			if ip == nil || !ip.IsPrivate() {
				continue
			}
			out = append(out, ip.String())
		}
	}
	// Home networks are overwhelmingly 192.168.x, and virtual adapters (VMware,
	// WSL, Docker) hand out 172.x / 10.x — so prefer the address a phone can
	// actually reach.
	// The routed address first: virtual adapters (VMware, WSL, hotspot) hand out
	// private IPs too, and those links go nowhere from a phone.
	main := primaryIP()
	sort.SliceStable(out, func(i, j int) bool {
		if (out[i] == main) != (out[j] == main) {
			return out[i] == main
		}
		return prefixRank(out[i]) < prefixRank(out[j])
	})
	if main != "" {
		// One address is all anyone needs; the rest are noise.
		return []string{main}
	}
	return out
}

func prefixRank(ip string) int {
	switch {
	case len(ip) >= 8 && ip[:8] == "192.168.":
		return 0
	case len(ip) >= 3 && ip[:3] == "10.":
		return 1
	default:
		return 2 // 172.16-31: usually a virtual adapter
	}
}

// lanURLs builds ready-to-tap links, token included, so nothing has to be typed
// and they stay correct when DHCP moves the machine.
func lanURLs(port int, token string) []string {
	var urls []string
	for _, ip := range lanIPs() {
		urls = append(urls, fmt.Sprintf("http://%s:%d/?t=%s", ip, port, token))
	}
	return urls
}

// listenAddr binds every interface so both the tunnel and the LAN can reach it.
func listenAddr(c Config) string {
	_, port, err := net.SplitHostPort(c.Addr)
	if err != nil {
		port = "7070"
	}
	return ":" + port
}

func listenPort(c Config) int {
	_, port, err := net.SplitHostPort(c.Addr)
	if err != nil {
		return 7070
	}
	var p int
	fmt.Sscanf(port, "%d", &p)
	if p == 0 {
		p = 7070
	}
	return p
}
