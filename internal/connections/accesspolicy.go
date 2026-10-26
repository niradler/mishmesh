package connections

import (
	"errors"
	"fmt"
	"net/mail"
	"regexp"
	"strings"
	"time"

	"github.com/mishmesh/mishmesh/internal/store"
)

const (
	maxAccessPolicyEntries = 100
	maxAccessSession       = 730 * time.Hour
)

var accessGroupPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

type AccessPolicyProvider interface {
	ValidateAccessPolicy(Config) error
}

func NormalizeAccessPolicy(policy *store.AccessPolicy) error {
	if policy == nil {
		return nil
	}
	if len(policy.AllowEmails)+len(policy.AllowEmailDomains)+len(policy.AllowGroups) == 0 {
		return errors.New("access policy needs at least one of allow_emails, allow_email_domains or allow_groups")
	}
	var err error
	if policy.AllowEmails, err = normalizeAccessList("allow_emails", policy.AllowEmails, normalizeAccessEmail); err != nil {
		return err
	}
	if policy.AllowEmailDomains, err = normalizeAccessList("allow_email_domains", policy.AllowEmailDomains, normalizeAccessDomain); err != nil {
		return err
	}
	if policy.AllowGroups, err = normalizeAccessList("allow_groups", policy.AllowGroups, normalizeAccessGroup); err != nil {
		return err
	}
	if policy.SessionDuration != "" {
		duration, parseErr := time.ParseDuration(policy.SessionDuration)
		if parseErr != nil || duration < time.Minute || duration > maxAccessSession {
			return errors.New("access policy session_duration must be a duration between 1m and 730h")
		}
	}
	return nil
}

func normalizeAccessList(field string, values []string, normalize func(string) (string, bool)) ([]string, error) {
	if len(values) > maxAccessPolicyEntries {
		return nil, fmt.Errorf("access policy %s allows at most %d entries", field, maxAccessPolicyEntries)
	}
	seen := make(map[string]bool, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		normalized, ok := normalize(value)
		if !ok {
			return nil, fmt.Errorf("access policy %s has an invalid entry %q", field, value)
		}
		if seen[normalized] {
			return nil, fmt.Errorf("access policy %s has a duplicate entry %q", field, value)
		}
		seen[normalized] = true
		out = append(out, normalized)
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

func normalizeAccessEmail(value string) (string, bool) {
	value = strings.ToLower(strings.TrimSpace(value))
	address, err := mail.ParseAddress(value)
	if err != nil || address.Name != "" || address.Address != value {
		return "", false
	}
	_, domain, found := strings.Cut(value, "@")
	if !found {
		return "", false
	}
	if _, ok := normalizeAccessDomain(domain); !ok {
		return "", false
	}
	return value, true
}

func normalizeAccessDomain(value string) (string, bool) {
	domain, err := NormalizeAccessHostname(value)
	if err != nil || !strings.Contains(domain, ".") {
		return "", false
	}
	return domain, true
}

func normalizeAccessGroup(value string) (string, bool) {
	value = strings.TrimSpace(value)
	return value, accessGroupPattern.MatchString(value)
}
