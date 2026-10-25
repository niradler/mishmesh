package decryptscope

import (
	"errors"
	"fmt"
	"net"
	"slices"
	"strings"
)

const (
	MaxPatterns    = 256
	maxHostnameLen = 253
	maxLabelLen    = 63
	wildcardPrefix = "*."
)

var ErrTooMany = fmt.Errorf("decryptscope: at most %d patterns per list", MaxPatterns)

func Normalize(patterns []string) ([]string, error) {
	if len(patterns) > MaxPatterns {
		return nil, ErrTooMany
	}
	out := make([]string, 0, len(patterns))
	for _, raw := range patterns {
		p, err := normalizePattern(raw)
		if err != nil {
			return nil, err
		}
		if !slices.Contains(out, p) {
			out = append(out, p)
		}
	}
	slices.Sort(out)
	return out, nil
}

func normalizePattern(raw string) (string, error) {
	p := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(raw)), ".")
	host := strings.TrimPrefix(p, wildcardPrefix)
	if net.ParseIP(strings.Trim(host, "[]")) != nil {
		return "", fmt.Errorf("decryptscope: %q is an IP address; only DNS names can be decrypted", raw)
	}
	if !ValidHostname(host) {
		return "", fmt.Errorf("decryptscope: %q must be a DNS name or *.name", raw)
	}
	return p, nil
}

func ValidHostname(host string) bool {
	if host == "" || len(host) > maxHostnameLen {
		return false
	}
	for label := range strings.SplitSeq(host, ".") {
		if label == "" || len(label) > maxLabelLen || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, r := range label {
			if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '-' {
				return false
			}
		}
	}
	return true
}

func CanonicalHost(host string) (string, bool) {
	h := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(host)), ".")
	if net.ParseIP(strings.Trim(h, "[]")) != nil || !ValidHostname(h) {
		return "", false
	}
	return h, true
}

func Decrypts(allow, bypass []string, host string) bool {
	h, ok := CanonicalHost(host)
	if !ok {
		return false
	}
	for _, p := range bypass {
		if bypassMatches(p, h) {
			return false
		}
	}
	return matchesCanonical(allow, h)
}

func Matches(patterns []string, host string) bool {
	h, ok := CanonicalHost(host)
	return ok && matchesCanonical(patterns, h)
}

func matchesCanonical(patterns []string, host string) bool {
	for _, p := range patterns {
		if allowMatches(p, host) {
			return true
		}
	}
	return false
}

func allowMatches(pattern, host string) bool {
	if base, ok := strings.CutPrefix(pattern, wildcardPrefix); ok {
		return strings.HasSuffix(host, "."+base)
	}
	return host == pattern
}

func bypassMatches(pattern, host string) bool {
	if base, ok := strings.CutPrefix(pattern, wildcardPrefix); ok {
		return strings.HasSuffix(host, "."+base)
	}
	return host == pattern || strings.HasSuffix(host, "."+pattern)
}

type ConstraintSet struct {
	Permitted []string
	Excluded  []string
}

func ConstraintGroups(allow, bypass []string) ([]ConstraintSet, error) {
	if len(allow) == 0 {
		return nil, errors.New("decryptscope: an empty allowlist permits nothing")
	}
	ordered := slices.Clone(allow)
	slices.SortStableFunc(ordered, func(a, b string) int {
		return strings.Count(strings.TrimPrefix(a, wildcardPrefix), ".") - strings.Count(strings.TrimPrefix(b, wildcardPrefix), ".")
	})
	var groups [][]string
	for _, p := range ordered {
		placed := false
		for i, members := range groups {
			if !slices.ContainsFunc(members, func(m string) bool { return conflicts(allow, m, p) }) {
				groups[i] = append(members, p)
				placed = true
				break
			}
		}
		if !placed {
			groups = append(groups, []string{p})
		}
	}
	bypassExcluded := make([]string, 0, len(bypass))
	for _, p := range bypass {
		if base, ok := strings.CutPrefix(p, wildcardPrefix); ok {
			bypassExcluded = append(bypassExcluded, "."+base)
			continue
		}
		bypassExcluded = append(bypassExcluded, p)
	}
	out := make([]ConstraintSet, 0, len(groups))
	for _, members := range groups {
		var set ConstraintSet
		for _, p := range members {
			if base, ok := strings.CutPrefix(p, wildcardPrefix); ok {
				set.Permitted = append(set.Permitted, "."+base)
				continue
			}
			set.Permitted = append(set.Permitted, p)
			if !subdomainsAllowed(allow, p) {
				set.Excluded = append(set.Excluded, "."+p)
			}
		}
		set.Excluded = append(set.Excluded, bypassExcluded...)
		out = append(out, set)
	}
	return out, nil
}

func conflicts(allow []string, a, b string) bool {
	return excludesSubtreeOf(allow, a, b) || excludesSubtreeOf(allow, b, a)
}

func excludesSubtreeOf(allow []string, exact, other string) bool {
	if strings.HasPrefix(exact, wildcardPrefix) || subdomainsAllowed(allow, exact) {
		return false
	}
	return strings.HasSuffix(strings.TrimPrefix(other, wildcardPrefix), "."+exact)
}

func subdomainsAllowed(allow []string, exact string) bool {
	for _, p := range allow {
		if base, ok := strings.CutPrefix(p, wildcardPrefix); ok && (base == exact || strings.HasSuffix(exact, "."+base)) {
			return true
		}
	}
	return false
}
