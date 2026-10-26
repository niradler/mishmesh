package cloudflare

import "context"

func (p *Publisher) reconcileAccess(ctx context.Context, entries []*hostEntry) error {
	app, err := p.client.GetAccessApp(ctx, p.cfg.AccountID, p.cfg.AccessAppID)
	if err != nil {
		for _, e := range entries {
			if e.own != nil {
				continue
			}
			e.Access = AccessError
			if e.Detail == "" {
				e.Detail = err.Error()
			}
		}
		return err
	}
	var touched []*hostEntry
	for _, e := range entries {
		switch {
		case e.own != nil:
			if e.own.exists && p.releasesSharedAccess(app, e.Host) && accessRemoveHost(app, e.Host) {
				e.accessReleased = true
				touched = append(touched, e)
			}
		case e.orphan && p.cfg.Prune && !e.Ingress && !e.failed:
			if p.ownership != nil && p.ownership.state.Routes[e.Host].Access != accessFingerprint(app, e.Host) {
				e.Access = accessLabel(accessHasHost(app, e.Host))
				continue
			}
			if accessRemoveHost(app, e.Host) {
				touched = append(touched, e)
			}
			e.Access = AccessMissing
		case e.orphan:
			e.Access = accessLabel(accessHasHost(app, e.Host))
		case e.conflict:
			e.Access = accessLabel(accessHasHost(app, e.Host))
		default:
			if accessAddHost(app, e.Host) {
				e.accessApplied = true
				touched = append(touched, e)
			}
			e.Access = AccessOK
		}
	}
	if len(touched) == 0 {
		p.recordAccess(entries, app)
		return nil
	}
	if err := p.client.PutAccessApp(ctx, p.cfg.AccountID, p.cfg.AccessAppID, app); err != nil {
		for _, e := range touched {
			e.Access = AccessError
		}
		return err
	}
	if p.ownership != nil {
		observed, err := p.client.GetAccessApp(ctx, p.cfg.AccountID, p.cfg.AccessAppID)
		if err != nil {
			for _, entry := range touched {
				entry.Access = AccessError
			}
			return err
		}
		app = observed
	}
	p.recordAccess(entries, app)
	return nil
}

func (p *Publisher) recordAccess(entries []*hostEntry, app AccessApp) {
	if p.ownership == nil {
		return
	}
	for _, entry := range entries {
		p.ownership.update(entry.Host, func(owned *ownedRoute) {
			if !entry.orphan && !entry.conflict && !entry.failed && entry.Access == AccessOK && entry.accessApplied {
				owned.Access = accessFingerprint(app, entry.Host)
			} else if entry.accessReleased || entry.orphan && entry.Access == AccessMissing && !entry.Ingress && !entry.failed {
				owned.Access = ""
			}
		})
	}
}

func (p *Publisher) releasesSharedAccess(app AccessApp, host string) bool {
	if p.ownership == nil {
		return false
	}
	recorded := p.ownership.state.Routes[host].Access
	return recorded != "" && recorded == accessFingerprint(app, host)
}

func accessLabel(present bool) string {
	if present {
		return AccessOK
	}
	return AccessMissing
}
