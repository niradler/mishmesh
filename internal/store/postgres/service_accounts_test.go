package postgres

import (
	"testing"

	"github.com/mishmesh/mishmesh/internal/store"
	"github.com/mishmesh/mishmesh/internal/store/storetest"
)

func TestServiceAccounts(t *testing.T) {
	storetest.RunServiceAccounts(t, func(t *testing.T) store.DataStore { return newStore(t) })
}

func TestSignupClaimingInvite(t *testing.T) {
	storetest.RunSignupClaimingInvite(t, func(t *testing.T) store.DataStore { return newStore(t) })
}

func TestListAllEndpoints(t *testing.T) {
	storetest.RunListAllEndpoints(t, func(t *testing.T) store.DataStore { return newStore(t) })
}

func TestGetUsersByIDs(t *testing.T) {
	storetest.RunGetUsersByIDs(t, func(t *testing.T) store.DataStore { return newStore(t) })
}

func TestKeepOwner(t *testing.T) {
	storetest.RunKeepOwner(t, func(t *testing.T) store.DataStore { return newStore(t) })
}

func TestMembershipDisable(t *testing.T) {
	storetest.RunMembershipDisable(t, func(t *testing.T) store.DataStore { return newStore(t) })
}

func TestAccessLog(t *testing.T) {
	storetest.RunAccessLog(t, func(t *testing.T) store.DataStore { return newStore(t) })
}
