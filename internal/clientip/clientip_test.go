package clientip

import (
	"net"
	"net/http/httptest"
	"testing"
)

func mustNets(t *testing.T, cidrs ...string) []*net.IPNet {
	t.Helper()
	var out []*net.IPNet
	for _, c := range cidrs {
		_, n, err := net.ParseCIDR(c)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, n)
	}
	return out
}

func TestResolve(t *testing.T) {
	trusted := mustNets(t, "10.0.0.0/8", "192.168.0.0/16")
	cases := []struct {
		name    string
		remote  string
		xff     []string
		trusted []*net.IPNet
		want    string
	}{
		{"no trusted proxies ignores xff", "203.0.113.9:1234", []string{"1.2.3.4"}, nil, "203.0.113.9"},
		{"untrusted peer ignores xff", "203.0.113.9:1234", []string{"1.2.3.4"}, trusted, "203.0.113.9"},
		{"trusted peer uses xff", "10.0.0.1:80", []string{"198.51.100.7"}, trusted, "198.51.100.7"},
		{"rightmost untrusted wins over spoofed left", "10.0.0.1:80", []string{"6.6.6.6, 198.51.100.7"}, trusted, "198.51.100.7"},
		{"skips trusted hops", "10.0.0.1:80", []string{"198.51.100.7, 10.0.0.2, 192.168.1.1"}, trusted, "198.51.100.7"},
		{"multiple header lines", "10.0.0.1:80", []string{"6.6.6.6", "198.51.100.7, 10.0.0.2"}, trusted, "198.51.100.7"},
		{"trusted peer without xff", "10.0.0.1:80", nil, trusted, "10.0.0.1"},
		{"all hops trusted uses leftmost", "10.0.0.1:80", []string{"10.0.0.5, 10.0.0.2"}, trusted, "10.0.0.5"},
		{"garbage hop falls back to peer", "10.0.0.1:80", []string{"198.51.100.7, nonsense"}, trusted, "10.0.0.1"},
		{"ipv6 client", "10.0.0.1:80", []string{"2001:db8::1"}, trusted, "2001:db8::1"},
		{"remote without port", "10.0.0.1", []string{"198.51.100.7"}, trusted, "198.51.100.7"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := httptest.NewRequest("GET", "/", nil)
			r.RemoteAddr = c.remote
			for _, v := range c.xff {
				r.Header.Add("X-Forwarded-For", v)
			}
			got := Resolve(r, c.trusted)
			if got == nil || got.String() != c.want {
				t.Fatalf("got %v want %s", got, c.want)
			}
		})
	}
}
