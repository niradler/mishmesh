package agent

import (
	"net"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/mishmesh/mishmesh/internal/store"
)

type egressConfig struct {
	Listen      string    `yaml:"listen"`
	SocksListen string    `yaml:"socks_listen"`
	RequireAuth bool      `yaml:"require_auth"`
	Exit        string    `yaml:"exit"`
	ServeExit   bool      `yaml:"serve_exit"`
	ExitAllow   []string  `yaml:"exit_allow"`
	Originate   yaml.Node `yaml:"originate"`
}

type originateConfig struct {
	Listen   string `yaml:"listen"`
	Upstream string `yaml:"upstream"`
	Token    string `yaml:"token"`
}

func (p *configParser) parseEgress(n *yaml.Node, connects []ConnectSpec) *EgressSpec {
	p.checkKeys(n, egressKeys, "egress")
	var ec egressConfig
	if err := n.Decode(&ec); err != nil {
		p.addf(n.Line, "egress: %s", yamlProblem(err))
		return nil
	}
	spec := &EgressSpec{RequireAuth: ec.RequireAuth, Exit: strings.TrimSpace(ec.Exit), ServeExit: ec.ServeExit}
	switch {
	case strings.TrimSpace(ec.Listen) != "":
		spec.Listen = normalizeListen(ec.Listen)
	case strings.TrimSpace(ec.SocksListen) == "" && !ec.ServeExit && ec.Originate.Kind == 0:
		spec.Listen = DefaultEgressListen
	}
	if strings.TrimSpace(ec.SocksListen) != "" {
		spec.SocksListen = normalizeListen(ec.SocksListen)
	}
	used := map[string]string{}
	for _, c := range connects {
		if c.Kind == store.KindTCP {
			used[c.Listen] = "connect " + strconv.Quote(c.Name)
		}
	}
	for _, l := range []struct{ key, raw, addr string }{{"listen", ec.Listen, spec.Listen}, {"socks_listen", ec.SocksListen, spec.SocksListen}} {
		if l.addr == "" {
			continue
		}
		if _, _, err := net.SplitHostPort(l.addr); err != nil {
			p.addf(n.Line, "egress: %s %q must be a port or host:port", l.key, l.raw)
			continue
		}
		if owner, taken := used[l.addr]; taken {
			p.addf(n.Line, "egress: %s %s is already used by %s", l.key, l.addr, owner)
		}
		used[l.addr] = "egress " + l.key
	}
	spec.Originate = p.parseOriginate(&ec.Originate, spec.RequireAuth, used)
	for _, c := range ec.ExitAllow {
		cidr := strings.TrimSpace(c)
		if _, _, err := net.ParseCIDR(cidr); err != nil {
			p.addf(n.Line, "egress: exit_allow %q is not a CIDR", c)
			continue
		}
		spec.ExitAllow = append(spec.ExitAllow, cidr)
	}
	switch {
	case len(ec.ExitAllow) > 0 && !spec.ServeExit:
		p.addf(n.Line, "egress: exit_allow needs serve_exit")
	case spec.RequireAuth && spec.SocksListen != "":
		p.addf(n.Line, "egress: require_auth cannot cover socks_listen: SOCKS5 passwords are capped at 255 bytes, shorter than mishmesh tokens")
	case spec.RequireAuth && spec.Listen == "":
		p.addf(n.Line, "egress: require_auth needs listen")
	}
	return spec
}

func (p *configParser) parseOriginate(n *yaml.Node, requireAuth bool, used map[string]string) []OriginateSpec {
	if !p.optionalMapping(n, "egress: originate must be a mapping of name to originate definition") {
		return nil
	}
	var out []OriginateSpec
	p.eachNamed(n, "egress", "originate", originateKeys, func(name string, _, val *yaml.Node) {
		var oc originateConfig
		if err := val.Decode(&oc); err != nil {
			p.addf(val.Line, "egress: originate %q: %s", name, yamlProblem(err))
			return
		}
		spec := OriginateSpec{Name: name, Listen: normalizeListen(oc.Listen), Upstream: strings.TrimSpace(oc.Upstream), Token: strings.TrimSpace(oc.Token)}
		if spec.Listen == "" {
			p.addf(val.Line, "egress: originate %q needs listen", name)
			return
		}
		if _, _, err := net.SplitHostPort(spec.Listen); err != nil {
			p.addf(val.Line, "egress: originate %q listen %q must be a port or host:port", name, oc.Listen)
			return
		}
		if owner, taken := used[spec.Listen]; taken {
			p.addf(val.Line, "egress: originate %q listen %s is already used by %s", name, spec.Listen, owner)
			return
		}
		used[spec.Listen] = "egress originate " + strconv.Quote(name)
		if _, err := parseOriginTarget(spec.Upstream); err != nil {
			p.addf(val.Line, "egress: originate %q upstream %q %v", name, oc.Upstream, err)
			return
		}
		if requireAuth && spec.Token == "" {
			p.addf(val.Line, "egress: originate %q needs token because require_auth is set", name)
			return
		}
		out = append(out, spec)
	})
	return out
}
