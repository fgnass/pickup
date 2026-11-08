package main

import (
	"errors"
	"net"
	"strings"
)

// pickIP finds the IPv4 address of the local network (Wi-Fi or Ethernet).
// It prefers the "en*" interface that carries the default route, so a VPN
// does not take over.
func pickIP(override string) (net.IP, error) {
	if override != "" {
		ip := net.ParseIP(override).To4()
		if ip == nil {
			return nil, errors.New("invalid -ip address")
		}
		return ip, nil
	}

	var route net.IP
	// Dialing UDP sends no packets. It only asks the kernel for the source
	// address of the default route. 192.0.2.1 is a documentation address.
	if c, err := net.Dial("udp4", "192.0.2.1:9"); err == nil {
		route = c.LocalAddr().(*net.UDPAddr).IP.To4()
		c.Close()
	}

	var lan []net.IP
	ifaces, _ := net.Interfaces()
	for _, ifc := range ifaces {
		if ifc.Flags&net.FlagUp == 0 || ifc.Flags&net.FlagLoopback != 0 || !strings.HasPrefix(ifc.Name, "en") {
			continue
		}
		addrs, _ := ifc.Addrs()
		for _, a := range addrs {
			if n, ok := a.(*net.IPNet); ok {
				if ip := n.IP.To4(); ip != nil && ip.IsPrivate() {
					lan = append(lan, ip)
				}
			}
		}
	}
	for _, ip := range lan {
		if ip.Equal(route) {
			return ip, nil
		}
	}
	if len(lan) > 0 {
		return lan[0], nil
	}
	if route != nil && route.IsPrivate() {
		return route, nil
	}
	return nil, errors.New("no local network found. Connect to Wi-Fi and try again")
}
