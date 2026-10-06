package controlplane

import (
	"context"

	"github.com/mishmesh/mishmesh/internal/store"
)

const DefaultMaxOrgsPerUser = 3

func (a *API) SetMaxOrgsPerUser(n int) {
	a.maxOrgsPerUser = n
}

func (a *API) ownedOrgCount(ctx context.Context, userID string) (int, error) {
	ms, err := a.data.ListMembershipsByUser(ctx, userID)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, m := range ms {
		if m.Role == store.RoleOwner {
			n++
		}
	}
	return n, nil
}
