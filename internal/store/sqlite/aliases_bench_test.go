package sqlite

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/mishmesh/mishmesh/internal/store"
)

func BenchmarkReplaceConnectionAliases(b *testing.B) {
	const aliasCount = 100
	s := newStore(b)
	ctx := context.Background()
	orgID := createTypedOrg(b, s)
	connection := createTypedConnection(b, s, orgID)
	aliases := make([]store.EndpointAlias, 0, aliasCount)
	for i := 0; i < aliasCount; i++ {
		name := fmt.Sprintf("host%d", i)
		suffix := strings.ReplaceAll(orgID, "_", "-")
		endpoint := createTypedEndpoint(b, s, orgID, "")
		endpoint.Policy = &store.EndpointPolicy{Access: []store.AccessRef{{ConnectionID: connection.ID, Hostname: name}}}
		if err := s.UpdateEndpoint(ctx, endpoint); err != nil {
			b.Fatal(err)
		}
		aliases = append(aliases, store.EndpointAlias{Hostname: name + "." + suffix + ".ts.net", AccessHostname: name, Kind: store.KindHTTP, EndpointID: endpoint.ID})
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := s.ReplaceConnectionAliases(ctx, orgID, connection.ID, aliases); err != nil {
			b.Fatal(err)
		}
	}
}
