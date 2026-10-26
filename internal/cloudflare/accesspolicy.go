package cloudflare

import (
	"context"
	"errors"
	"maps"
	"slices"
	"sort"
	"strings"

	"github.com/mishmesh/mishmesh/internal/store"
)

const (
	AccessAppShared = "shared"
	AccessAppOwn    = "own"

	AccessPolicyOK         = "ok"
	AccessPolicyDriftFixed = "drift_fixed"
	AccessPolicyError      = "error"

	accessNamePrefix = "mishmesh "
)

var managedIncludeKinds = []string{"email", "email_domain", "group"}

type ownAccess struct {
	exists bool
	status string
	detail string
}

func accessObjectName(host string) string {
	return accessNamePrefix + host
}

func accessObjectID(object map[string]any) string {
	id, _ := object["id"].(string)
	if !providerIDPattern.MatchString(id) {
		return ""
	}
	return id
}

func accessRuleKind(rule any) string {
	object, ok := rule.(map[string]any)
	if !ok || len(object) != 1 {
		return ""
	}
	for kind := range object {
		return kind
	}
	return ""
}

func isMFARule(rule any) bool {
	object, ok := rule.(map[string]any)
	if !ok || accessRuleKind(rule) != "auth_method" {
		return false
	}
	method, _ := object["auth_method"].(map[string]any)
	value, _ := method["auth_method"].(string)
	return value == "mfa"
}

func desiredIncludeRules(policy *store.AccessPolicy) []any {
	rules := make([]any, 0, len(policy.AllowEmails)+len(policy.AllowEmailDomains)+len(policy.AllowGroups))
	for _, email := range policy.AllowEmails {
		rules = append(rules, map[string]any{"email": map[string]any{"email": email}})
	}
	for _, domain := range policy.AllowEmailDomains {
		rules = append(rules, map[string]any{"email_domain": map[string]any{"domain": domain}})
	}
	for _, group := range policy.AllowGroups {
		rules = append(rules, map[string]any{"group": map[string]any{"id": group}})
	}
	return rules
}

func mergeAccessPolicy(observed AccessPolicy, host string, policy *store.AccessPolicy) AccessPolicy {
	merged := maps.Clone(observed)
	if merged == nil {
		merged = AccessPolicy{"exclude": []any{}}
	}
	merged["name"], merged["decision"] = accessObjectName(host), "allow"
	include := make([]any, 0)
	existing, _ := observed["include"].([]any)
	for _, rule := range existing {
		if !slices.Contains(managedIncludeKinds, accessRuleKind(rule)) {
			include = append(include, rule)
		}
	}
	merged["include"] = append(include, desiredIncludeRules(policy)...)
	require := make([]any, 0)
	existing, _ = observed["require"].([]any)
	for _, rule := range existing {
		if !isMFARule(rule) {
			require = append(require, rule)
		}
	}
	if policy.RequireMFA {
		require = append(require, map[string]any{"auth_method": map[string]any{"auth_method": "mfa"}})
	}
	merged["require"] = require
	return merged
}

func accessPolicyProjection(policy AccessPolicy) string {
	managed := make([]string, 0)
	include, _ := policy["include"].([]any)
	for _, rule := range include {
		if slices.Contains(managedIncludeKinds, accessRuleKind(rule)) {
			managed = append(managed, resourceFingerprint(rule))
		}
	}
	sort.Strings(managed)
	require, _ := policy["require"].([]any)
	return resourceFingerprint(map[string]any{"name": policy["name"], "decision": policy["decision"], "include": managed, "mfa": slices.ContainsFunc(require, isMFARule)})
}

func accessAppLinks(app AccessApp, policyID string) bool {
	policies, _ := app["policies"].([]any)
	for _, entry := range policies {
		switch value := entry.(type) {
		case string:
			if value == policyID {
				return true
			}
		case map[string]any:
			if id, _ := value["id"].(string); id == policyID {
				return true
			}
		}
	}
	return false
}

func accessPolicyRefs(app AccessApp, policyID string) []any {
	policies, _ := app["policies"].([]any)
	refs := make([]any, 0, len(policies)+1)
	for _, entry := range policies {
		switch value := entry.(type) {
		case string:
			refs = append(refs, map[string]any{"id": value})
		case map[string]any:
			if id, _ := value["id"].(string); id != "" {
				ref := map[string]any{"id": id}
				if precedence, found := value["precedence"]; found {
					ref["precedence"] = precedence
				}
				refs = append(refs, ref)
			}
		}
	}
	if !accessAppLinks(app, policyID) {
		refs = append(refs, map[string]any{"id": policyID, "precedence": len(refs) + 1})
	}
	return refs
}

func mergeAccessApp(observed AccessApp, host, policyID string, policy *store.AccessPolicy) AccessApp {
	merged := maps.Clone(observed)
	if merged == nil {
		merged = AccessApp{"destinations": []any{map[string]any{"type": "public", "uri": host}}}
	}
	merged["name"], merged["type"], merged["domain"] = accessObjectName(host), "self_hosted", host
	if policy.SessionDuration != "" {
		merged["session_duration"] = policy.SessionDuration
	}
	accessAddHost(merged, host)
	merged["policies"] = accessPolicyRefs(merged, policyID)
	return merged
}

func accessAppProjection(app AccessApp, host, policyID string) string {
	domain, _ := app["domain"].(string)
	return resourceFingerprint(map[string]any{"name": app["name"], "type": app["type"], "domain": strings.ToLower(domain), "session_duration": app["session_duration"],
		"host": accessHasHost(app, host), "linked": accessAppLinks(app, policyID)})
}

func (p *Publisher) reconcileOwnAccess(ctx context.Context, hosts []Hostname) (map[string]*ownAccess, error) {
	results := make(map[string]*ownAccess)
	var errs []error
	for _, host := range hosts {
		if host.Policy == nil {
			continue
		}
		if p.ownership == nil {
			results[host.Host] = &ownAccess{status: AccessPolicyError, detail: "access policies need a managed Cloudflare connection"}
			continue
		}
		result, err := p.ensureOwnAccess(ctx, host)
		results[host.Host] = result
		if err != nil {
			errs = append(errs, err)
		}
	}
	return results, errors.Join(errs...)
}

func (p *Publisher) ownedApp(host string) ownedAccessApp {
	if owned := p.ownership.state.Routes[host].App; owned != nil {
		return *owned
	}
	return ownedAccessApp{}
}

func (p *Publisher) storeOwnedApp(host string, record ownedAccessApp) {
	p.ownership.update(host, func(owned *ownedRoute) {
		if record == (ownedAccessApp{}) {
			owned.App = nil
		} else {
			owned.App = &record
		}
	})
}

func (p *Publisher) ensureOwnAccess(ctx context.Context, host Hostname) (*ownAccess, error) {
	record := p.ownedApp(host.Host)
	failed := func(err error) (*ownAccess, error) {
		p.storeOwnedApp(host.Host, record)
		return &ownAccess{exists: record.AppID != "", status: AccessPolicyError, detail: err.Error()}, err
	}
	drift := false
	var observedPolicy AccessPolicy
	if record.PolicyID != "" {
		current, err := p.client.GetAccessPolicy(ctx, p.cfg.AccountID, record.PolicyID)
		switch {
		case isNotFound(err):
			record.PolicyID, record.Policy, drift = "", "", true
		case err != nil:
			return failed(err)
		default:
			observedPolicy = current
		}
	}
	if record.PolicyID == "" {
		created, err := p.client.CreateAccessPolicy(ctx, p.cfg.AccountID, mergeAccessPolicy(nil, host.Host, host.Policy))
		if err != nil {
			return failed(err)
		}
		if record.PolicyID = accessObjectID(created); record.PolicyID == "" {
			return failed(errors.New("created access policy has no valid ID"))
		}
		observedPolicy = created
	} else {
		if accessPolicyProjection(observedPolicy) != record.Policy {
			drift = true
		}
		merged := mergeAccessPolicy(observedPolicy, host.Host, host.Policy)
		if accessPolicyProjection(merged) != accessPolicyProjection(observedPolicy) {
			if err := p.client.PutAccessPolicy(ctx, p.cfg.AccountID, record.PolicyID, merged); err != nil {
				return failed(err)
			}
		}
		observedPolicy = merged
	}
	record.Policy = accessPolicyProjection(observedPolicy)

	var observedApp AccessApp
	if record.AppID != "" {
		current, err := p.client.GetAccessApp(ctx, p.cfg.AccountID, record.AppID)
		switch {
		case isNotFound(err):
			record.AppID, record.App, drift = "", "", true
		case err != nil:
			return failed(err)
		default:
			observedApp = current
		}
	}
	if record.AppID == "" {
		created, err := p.client.CreateAccessApp(ctx, p.cfg.AccountID, mergeAccessApp(nil, host.Host, record.PolicyID, host.Policy))
		if err != nil {
			return failed(err)
		}
		if record.AppID = accessObjectID(created); record.AppID == "" {
			return failed(errors.New("created access app has no valid ID"))
		}
		observedApp = created
	} else {
		if accessAppProjection(observedApp, host.Host, record.PolicyID) != record.App {
			drift = true
		}
		merged := mergeAccessApp(observedApp, host.Host, record.PolicyID, host.Policy)
		if accessAppProjection(merged, host.Host, record.PolicyID) != accessAppProjection(observedApp, host.Host, record.PolicyID) {
			if err := p.client.PutAccessApp(ctx, p.cfg.AccountID, record.AppID, merged); err != nil {
				return failed(err)
			}
		}
		observedApp = merged
	}
	record.App = accessAppProjection(observedApp, host.Host, record.PolicyID)
	p.storeOwnedApp(host.Host, record)
	result := &ownAccess{exists: true, status: AccessPolicyOK}
	if drift {
		result.status, result.detail = AccessPolicyDriftFixed, "managed Access settings were changed in Cloudflare and have been restored"
		p.log.Warn("cloudflare access drift fixed", "host", host.Host)
	}
	return result, nil
}

func (p *Publisher) removeOwnAccess(ctx context.Context, host string) error {
	record := p.ownedApp(host)
	if record.AppID != "" {
		if err := p.client.DeleteAccessApp(ctx, p.cfg.AccountID, record.AppID); err != nil && !isNotFound(err) {
			return err
		}
		record.AppID, record.App = "", ""
		p.storeOwnedApp(host, record)
	}
	if record.PolicyID != "" {
		if err := p.client.DeleteAccessPolicy(ctx, p.cfg.AccountID, record.PolicyID); err != nil && !isNotFound(err) {
			return err
		}
		record.PolicyID, record.Policy = "", ""
		p.storeOwnedApp(host, record)
	}
	return nil
}

func (p *Publisher) pruneOwnAccess(ctx context.Context, entries []*hostEntry, own map[string]*ownAccess) error {
	if p.ownership == nil {
		return nil
	}
	byHost := make(map[string]*hostEntry, len(entries))
	for _, entry := range entries {
		byHost[entry.Host] = entry
	}
	hosts := make([]string, 0)
	for host, owned := range p.ownership.state.Routes {
		if owned.App != nil && own[host] == nil {
			hosts = append(hosts, host)
		}
	}
	sort.Strings(hosts)
	var errs []error
	for _, host := range hosts {
		entry := byHost[host]
		switch {
		case entry == nil:
			continue
		case !entry.orphan:
			if p.cfg.AccessAppID != "" && entry.Access != AccessOK {
				continue
			}
		case !p.cfg.Prune || entry.Ingress || entry.failed:
			continue
		}
		if err := p.removeOwnAccess(ctx, host); err != nil {
			errs = append(errs, err)
			if entry.Detail == "" {
				entry.Detail = err.Error()
			}
			entry.Access = AccessError
		}
	}
	return errors.Join(errs...)
}
