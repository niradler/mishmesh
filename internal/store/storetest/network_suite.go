package storetest

import (
	"testing"

	"github.com/mishmesh/mishmesh/internal/store"
)

func RunNetwork(t *testing.T, newStore func(*testing.T) store.DataStore) {
	cases := []struct {
		name string
		run  func(*testing.T, func(*testing.T) store.DataStore)
	}{
		{"ShouldEnforceRequirementDAG", shouldEnforceRequirementDAG},
		{"ShouldKeepMultipleConnectionsOfOneType", shouldKeepMultipleConnectionsOfOneType},
		{"ShouldReplaceTypedServiceReferencesAtomically", shouldReplaceTypedServiceReferencesAtomically},
		{"ShouldCreateTypedServiceWithReferencesAndRejectInvalidDocuments", shouldCreateTypedServiceWithReferencesAndRejectInvalidDocuments},
		{"ShouldValidateEndpointReferencesAndGuardDeletes", shouldValidateEndpointReferencesAndGuardDeletes},
		{"ShouldValidateAliasOwnersAndHostnameCollisions", shouldValidateAliasOwnersAndHostnameCollisions},
		{"ShouldSerializeConflictingAliasReplacements", shouldSerializeConflictingAliasReplacements},
		{"ServiceCRUD", serviceCRUD},
		{"ConnectionCRUDPreservesEncryptedConfiguration", connectionCRUDPreservesEncryptedConfiguration},
		{"ShouldRejectLateAliasPublicationAfterAccessEdit", shouldRejectLateAliasPublicationAfterAccessEdit},
		{"ShouldPreserveUnchangedAliasesAndRollbackInvalidEndpointPolicy", shouldPreserveUnchangedAliasesAndRollbackInvalidEndpointPolicy},
		{"ShouldFenceAliasPublicationByConnectionSnapshot", shouldFenceAliasPublicationByConnectionSnapshot},
		{"ShouldSerializeAccessHostnameClaimsPerConnection", shouldSerializeAccessHostnameClaimsPerConnection},
		{"ShouldFenceEndpointAccessByAuthorizedConnectionSnapshot", shouldFenceEndpointAccessByAuthorizedConnectionSnapshot},
		{"ShouldConditionConnectionMutationsOnAuthorizedUserFields", shouldConditionConnectionMutationsOnAuthorizedUserFields},
		{"ShouldGuardAuthorizedServiceSnapshots", shouldGuardAuthorizedServiceSnapshots},
		{"ShouldRollbackServiceMutationAndGuardTransitiveSnapshot", shouldRollbackServiceMutationAndGuardTransitiveSnapshot},
		{"ShouldSerializeServiceLeaseAgainstConcurrentMutation", shouldSerializeServiceLeaseAgainstConcurrentMutation},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) { tc.run(t, newStore) })
	}
}
