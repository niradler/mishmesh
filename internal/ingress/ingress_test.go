package ingress

import "testing"

func TestPathEndpoint(t *testing.T) {
	cases := []struct {
		path    string
		id      string
		outPath string
		ok      bool
	}{
		{"/tunnel/ep_1/foo/bar", "ep_1", "/foo/bar", true},
		{"/tunnel/ep_1", "ep_1", "/", true},
		{"/tunnel/ep_1/", "ep_1", "/", true},
		{"/tunnel/", "", "", false},
		{"/other/x", "", "", false},
		{"/", "", "", false},
	}
	for _, c := range cases {
		id, out, ok := pathEndpoint(c.path)
		if id != c.id || out != c.outPath || ok != c.ok {
			t.Errorf("pathEndpoint(%q) = (%q,%q,%v), want (%q,%q,%v)", c.path, id, out, ok, c.id, c.outPath, c.ok)
		}
	}
}

func TestSubdomain(t *testing.T) {
	i := &Ingress{apexHost: "localhost"}
	cases := []struct {
		host string
		sub  string
		ok   bool
	}{
		{"demo.localhost", "demo", true},
		{"demo.localhost:8080", "demo", true},
		{"localhost", "", false},
		{"localhost:8080", "", false},
		{"a.b.localhost", "", false},
		{"demo.example.com", "", false},
	}
	for _, c := range cases {
		sub, ok := i.subdomain(c.host)
		if sub != c.sub || ok != c.ok {
			t.Errorf("subdomain(%q) = (%q,%v), want (%q,%v)", c.host, sub, ok, c.sub, c.ok)
		}
	}
}
