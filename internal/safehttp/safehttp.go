package safehttp

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"time"
)

var metadataIP = net.IPv4(169, 254, 169, 254)

func Blocked(ip net.IP, allowLoopback, allowPrivate bool) bool {
	if ip.Equal(metadataIP) || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsMulticast() || ip.IsUnspecified() {
		return true
	}
	if ip.IsLoopback() {
		return !allowLoopback
	}
	if !allowPrivate {
		if ip.IsPrivate() {
			return true
		}
		if ip4 := ip.To4(); ip4 != nil && ip4[0] == 100 && ip4[1]&0xc0 == 64 {
			return true
		}
	}
	return false
}

func Client(allowLoopback, allowPrivate bool) *http.Client {
	dialer := &net.Dialer{Timeout: 10 * time.Second}
	transport := &http.Transport{
		ForceAttemptHTTP2: true,
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			host, port, err := net.SplitHostPort(addr)
			if err != nil {
				return nil, err
			}
			ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
			if err != nil {
				return nil, err
			}
			for _, ip := range ips {
				if Blocked(ip.IP, allowLoopback, allowPrivate) {
					return nil, fmt.Errorf("safehttp: host %q resolves to blocked address %s", host, ip.IP)
				}
			}
			return dialer.DialContext(ctx, network, net.JoinHostPort(ips[0].IP.String(), port))
		},
	}
	return &http.Client{
		Timeout:   10 * time.Second,
		Transport: transport,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return errors.New("safehttp: too many redirects")
			}
			if !allowLoopback && req.URL.Scheme != "https" {
				return fmt.Errorf("safehttp: insecure redirect scheme %q", req.URL.Scheme)
			}
			return nil
		},
	}
}
