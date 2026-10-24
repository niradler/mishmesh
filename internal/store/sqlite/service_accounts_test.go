package sqlite

import (
	"testing"

	"github.com/mishmesh/mishmesh/internal/store"
	"github.com/mishmesh/mishmesh/internal/store/storetest"
)

func TestServiceAccounts(t *testing.T) {
	storetest.RunServiceAccounts(t, func(t *testing.T) store.DataStore { return newStore(t) })
}
