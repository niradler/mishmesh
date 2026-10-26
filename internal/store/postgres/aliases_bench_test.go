package postgres

import (
	"testing"

	"github.com/mishmesh/mishmesh/internal/store"
	"github.com/mishmesh/mishmesh/internal/store/storetest"
)

func BenchmarkReplaceConnectionAliases(b *testing.B) {
	storetest.BenchReplaceConnectionAliases(b, func(tb testing.TB) store.DataStore { return newStore(tb) })
}
