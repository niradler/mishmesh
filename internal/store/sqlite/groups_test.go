package sqlite

import (
	"testing"

	"github.com/mishmesh/mishmesh/internal/store"
	"github.com/mishmesh/mishmesh/internal/store/storetest"
)

func TestGroups(t *testing.T) {
	storetest.RunGroups(t, func(t *testing.T) store.DataStore { return newStore(t) })
}

func TestOrgSecrets(t *testing.T) {
	storetest.RunOrgSecrets(t, func(t *testing.T) store.DataStore { return newStore(t) })
}

func TestWorkloadIdentities(t *testing.T) {
	storetest.RunWorkloadIdentities(t, func(t *testing.T) store.DataStore { return newStore(t) })
}

func TestEgressCredentials(t *testing.T) {
	storetest.RunEgressCredentials(t, func(t *testing.T) store.DataStore { return newStore(t) })
}

func TestEgressDecrypt(t *testing.T) {
	storetest.RunEgressDecrypt(t, func(t *testing.T) store.DataStore { return newStore(t) })
}

func TestMissingRowContract(t *testing.T) {
	storetest.RunMissingRows(t, func(t *testing.T) store.DataStore { return newStore(t) })
}

func TestNetworkContract(t *testing.T) {
	storetest.RunNetwork(t, func(t *testing.T) store.DataStore { return newStore(t) })
}
