package netblock

import "net"

var (
	alwaysBlockedNets = mustCIDRs(
		"0.0.0.0/8",
		"169.254.0.0/16",
		"224.0.0.0/4",
		"255.255.255.255/32",
		"fe80::/10",
		"ff00::/8",
		"fd00:ec2::254/128",
	)
	privateNets = mustCIDRs(
		"10.0.0.0/8",
		"172.16.0.0/12",
		"192.168.0.0/16",
		"100.64.0.0/10",
		"192.0.0.0/24",
		"198.18.0.0/15",
		"240.0.0.0/4",
		"fc00::/7",
		"64:ff9b::/96",
		"64:ff9b:1::/48",
		"fec0::/10",
		"2001::/32",
		"2002::/16",
	)
	embeddedIPv4Nets = mustCIDRs("64:ff9b::/96", "::/96")
)

func mustCIDRs(cidrs ...string) []*net.IPNet {
	out := make([]*net.IPNet, 0, len(cidrs))
	for _, c := range cidrs {
		_, n, err := net.ParseCIDR(c)
		if err != nil {
			panic(err)
		}
		out = append(out, n)
	}
	return out
}

type Policy struct {
	AllowLoopback bool
	AllowPrivate  bool
	AllowedCIDRs  []*net.IPNet
}

func Normalize(ip net.IP) net.IP {
	if v4 := ip.To4(); v4 != nil {
		return v4
	}
	return ip
}

func InAny(nets []*net.IPNet, ip net.IP) bool {
	for _, n := range nets {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

func (p Policy) IPAllowed(ip net.IP) bool {
	ip = Normalize(ip)
	if ip.IsUnspecified() || ip.IsMulticast() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || InAny(alwaysBlockedNets, ip) {
		return false
	}
	if ip.IsLoopback() {
		return p.AllowLoopback
	}
	if v4 := embeddedIPv4(ip); v4 != nil && !p.IPAllowed(v4) {
		return false
	}
	if ip.IsPrivate() || InAny(privateNets, ip) {
		return p.AllowPrivate || InAny(p.AllowedCIDRs, ip)
	}
	return true
}

func embeddedIPv4(ip net.IP) net.IP {
	if len(ip) != net.IPv6len || !InAny(embeddedIPv4Nets, ip) {
		return nil
	}
	return net.IPv4(ip[12], ip[13], ip[14], ip[15]).To4()
}
