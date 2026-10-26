package safehttp

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"time"

	"github.com/mishmesh/mishmesh/internal/netblock"
)

func Blocked(ip net.IP, allowLoopback, allowPrivate bool) bool {
	return !netblock.Policy{AllowLoopback: allowLoopback, AllowPrivate: allowPrivate}.IPAllowed(ip)
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
