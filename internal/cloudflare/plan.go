package cloudflare

import (
	"net/url"
	"sort"
	"strings"
)

const catchAllService = "http_status:404"

type ingressPlan struct {
	rules     []IngressRule
	changed   bool
	present   map[string]bool
	conflicts map[string]string
	added     map[string]bool
	orphans   []string
	removed   map[string]bool
}

func normalizeService(s string) string {
	u, err := url.Parse(strings.TrimSpace(s))
	if err != nil || u.Host == "" {
		return strings.TrimSpace(s)
	}
	out := strings.ToLower(u.Scheme) + "://" + strings.ToLower(u.Host) + strings.TrimRight(u.Path, "/")
	if u.RawQuery != "" {
		out += "?" + u.RawQuery
	}
	return out
}

func isCatchAll(r IngressRule) bool {
	return r.Hostname() == "" && r.Path() == ""
}

func planIngress(rules []IngressRule, origin string, desired []string, prune bool) ingressPlan {
	originKey := normalizeService(origin)
	plan := ingressPlan{
		present:   map[string]bool{},
		conflicts: map[string]string{},
		added:     map[string]bool{},
		removed:   map[string]bool{},
	}
	body := rules
	var catchAll *IngressRule
	if n := len(rules); n > 0 && isCatchAll(rules[n-1]) {
		last := rules[n-1]
		catchAll = &last
		body = rules[:n-1]
	}
	desiredSet := make(map[string]bool, len(desired))
	for _, h := range desired {
		desiredSet[h] = true
	}
	owned := map[string]bool{}
	foreign := map[string]string{}
	for _, r := range body {
		h := normalizeHost(r.Hostname())
		if h == "" {
			continue
		}
		if normalizeService(r.Service()) == originKey {
			owned[h] = true
		} else {
			foreign[h] = r.Service()
		}
	}
	for h := range owned {
		if !desiredSet[h] {
			plan.orphans = append(plan.orphans, h)
		}
	}
	sort.Strings(plan.orphans)

	out := make([]IngressRule, 0, len(body)+len(desired)+1)
	for _, r := range body {
		h := normalizeHost(r.Hostname())
		if prune && h != "" && !desiredSet[h] && normalizeService(r.Service()) == originKey {
			plan.removed[h] = true
			plan.changed = true
			continue
		}
		out = append(out, r)
	}
	for _, h := range desired {
		switch {
		case owned[h]:
			plan.present[h] = true
		case foreign[h] != "":
			plan.conflicts[h] = "hostname is already routed to " + foreign[h] + " in the tunnel configuration"
		default:
			out = append(out, NewIngressRule(h, origin))
			plan.added[h] = true
			plan.changed = true
		}
	}
	switch {
	case catchAll != nil:
		out = append(out, *catchAll)
	case len(out) > 0 || plan.changed:
		out = append(out, NewIngressRule("", catchAllService))
		plan.changed = true
	}
	plan.rules = out
	return plan
}
