package token

import "strings"

func Bearer(authorizationHeader string) string {
	scheme, rest, ok := strings.Cut(strings.TrimSpace(authorizationHeader), " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") {
		return ""
	}
	return strings.TrimSpace(rest)
}
