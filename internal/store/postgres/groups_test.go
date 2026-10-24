package postgres

import (
	"testing"

	"github.com/mishmesh/mishmesh/internal/store"
	"github.com/mishmesh/mishmesh/internal/store/storetest"
)

func TestGroups(t *testing.T) {
	storetest.RunGroups(t, func(t *testing.T) store.DataStore { return newStore(t) })
}
