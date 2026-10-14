package controlplane

import "github.com/mishmesh/mishmesh/internal/cloudflare"

func emptyCloudflareStatus() cloudflare.Status {
	return cloudflare.Status{Level: cloudflare.LevelNone, Connector: cloudflare.ConnectorStatus{EdgeLocations: []string{}}, Hostnames: []cloudflare.HostStatus{}}
}

func (a *API) cloudflareStatus(orgID string) cloudflare.Status {
	if a.legacyCloudflare == nil {
		return emptyCloudflareStatus()
	}
	return a.legacyCloudflare.Status(orgID)
}
