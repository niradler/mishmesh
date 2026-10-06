package subdomain

import (
	"fmt"
	"net"
	"regexp"
	"strings"
)

var label = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

var reserved = map[string]struct{}{
	"app": {}, "api": {}, "www": {}, "admin": {}, "login": {}, "auth": {},
	"mail": {}, "status": {}, "docs": {}, "static": {}, "assets": {}, "cdn": {},
	"connect": {}, "dashboard": {}, "console": {},
}

func Normalize(s string) string {
	return strings.ToLower(strings.TrimSpace(s))
}

func HostLabel(baseDomain string) string {
	host := baseDomain
	if h, _, err := net.SplitHostPort(baseDomain); err == nil {
		host = h
	}
	host = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(host), "."))
	first, _, _ := strings.Cut(host, ".")
	return first
}

func Validate(sub, baseDomain string) error {
	if !label.MatchString(sub) {
		return fmt.Errorf("invalid subdomain %q (use lowercase letters, digits and hyphens, max 63 characters)", sub)
	}
	if _, ok := reserved[sub]; ok {
		return fmt.Errorf("subdomain %q is reserved", sub)
	}
	if baseDomain != "" && sub == HostLabel(baseDomain) {
		return fmt.Errorf("subdomain %q is reserved", sub)
	}
	return nil
}
