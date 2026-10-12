package cloudflare

import "strings"

func accessDomains(app AccessApp) []string {
	list, _ := app["self_hosted_domains"].([]any)
	out := make([]string, 0, len(list))
	for _, v := range list {
		if s, ok := v.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

func accessHasDomain(app AccessApp, host string) bool {
	for _, d := range accessDomains(app) {
		if strings.EqualFold(d, host) {
			return true
		}
	}
	return false
}

func accessHasDestination(app AccessApp, host string) bool {
	list, _ := app["destinations"].([]any)
	for _, v := range list {
		m, ok := v.(map[string]any)
		if !ok {
			continue
		}
		if uri, _ := m["uri"].(string); strings.EqualFold(uri, host) {
			return true
		}
	}
	return false
}

func accessHasHost(app AccessApp, host string) bool {
	if !accessHasDomain(app, host) {
		return false
	}
	if _, ok := app["destinations"]; ok && !accessHasDestination(app, host) {
		return false
	}
	return true
}

func accessAddHost(app AccessApp, host string) bool {
	changed := false
	if !accessHasDomain(app, host) {
		list, _ := app["self_hosted_domains"].([]any)
		app["self_hosted_domains"] = append(list, host)
		changed = true
	}
	if dests, ok := app["destinations"]; ok && !accessHasDestination(app, host) {
		list, _ := dests.([]any)
		app["destinations"] = append(list, map[string]any{"type": "public", "uri": host})
		changed = true
	}
	return changed
}

func accessRemoveHost(app AccessApp, host string) bool {
	changed := false
	if list, ok := app["self_hosted_domains"].([]any); ok {
		kept := make([]any, 0, len(list))
		for _, v := range list {
			if s, isStr := v.(string); isStr && strings.EqualFold(s, host) {
				changed = true
				continue
			}
			kept = append(kept, v)
		}
		app["self_hosted_domains"] = kept
	}
	if list, ok := app["destinations"].([]any); ok {
		kept := make([]any, 0, len(list))
		for _, v := range list {
			if m, isMap := v.(map[string]any); isMap {
				if uri, _ := m["uri"].(string); strings.EqualFold(uri, host) {
					changed = true
					continue
				}
			}
			kept = append(kept, v)
		}
		app["destinations"] = kept
	}
	return changed
}
