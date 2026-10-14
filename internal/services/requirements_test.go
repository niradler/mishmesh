package services

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/mishmesh/mishmesh/internal/store"
)

func TestShouldValidateRequirementGraphs(t *testing.T) {
	graph := []*store.Service{
		{ID: "a", OrgID: "org", Name: "alpha", Requires: []string{"b", "c"}},
		{ID: "b", OrgID: "org", Name: "beta", Requires: []string{"d"}},
		{ID: "c", OrgID: "org", Name: "charlie", Requires: []string{"d"}},
		{ID: "d", OrgID: "org", Name: "delta"},
		{ID: "private-id", OrgID: "other", Name: "private-name", Requires: []string{"private-id"}},
	}
	tests := []struct {
		name, orgID, serviceID string
		requires               []string
		wantError              string
	}{
		{name: "empty", orgID: "org", serviceID: "a"},
		{name: "diamond", orgID: "org", serviceID: "a", requires: []string{"c", "b"}},
		{name: "set duplicates", orgID: "org", serviceID: "a", requires: []string{"b", "b", "c"}},
		{name: "new service", orgID: "org", serviceID: "new", requires: []string{"a"}},
		{name: "empty organization", serviceID: "a", wantError: "organization"},
		{name: "empty service", orgID: "org", wantError: "service"},
		{name: "empty requirement", orgID: "org", serviceID: "a", requires: []string{""}, wantError: "requirement"},
		{name: "missing requirement", orgID: "org", serviceID: "a", requires: []string{"missing-id"}, wantError: "requirement"},
		{name: "foreign requirement", orgID: "org", serviceID: "a", requires: []string{"private-id"}, wantError: "requirement"},
		{name: "self cycle", orgID: "org", serviceID: "a", requires: []string{"a"}, wantError: `"alpha" -> "alpha"`},
		{name: "long cycle", orgID: "org", serviceID: "d", requires: []string{"a"}, wantError: `"alpha" -> "beta" -> "delta" -> "alpha"`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := ValidateRequirements(test.orgID, test.serviceID, test.requires, graph)
			if test.wantError == "" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			if !errors.Is(err, store.ErrConflict) || !strings.Contains(err.Error(), test.wantError) {
				t.Fatalf("want conflict containing %q, got %v", test.wantError, err)
			}
			for _, secret := range []string{"private-id", "private-name", "missing-id"} {
				if strings.Contains(err.Error(), secret) {
					t.Fatalf("error leaked %q: %v", secret, err)
				}
			}
		})
	}
	if !reflect.DeepEqual(graph[0].Requires, []string{"b", "c"}) {
		t.Fatal("validator mutated snapshot")
	}
}

func TestShouldRejectCycleOnUpdate(t *testing.T) {
	graph := []*store.Service{{ID: "a", OrgID: "org", Requires: []string{"b"}}, {ID: "b", OrgID: "org"}}
	if err := ValidateRequirements("org", "b", []string{"a"}, graph); err == nil {
		t.Fatal("cycle accepted")
	}
}

func TestShouldValidateProspectiveReplacement(t *testing.T) {
	graph := []*store.Service{{ID: "a", OrgID: "org", Name: "alpha", Requires: []string{"b"}}, {ID: "b", OrgID: "org", Name: "beta", Requires: []string{"a"}}}
	if err := ValidateRequirements("org", "a", nil, graph); err != nil {
		t.Fatal(err)
	}
}

func TestShouldReportDeterministicRequirementCycles(t *testing.T) {
	graph := []*store.Service{{ID: "a", OrgID: "org", Name: "alpha", Requires: []string{"c", "b"}}, {ID: "b", OrgID: "org", Name: "beta", Requires: []string{"a"}}, {ID: "c", OrgID: "org", Name: "charlie", Requires: []string{"a"}}}
	first := ValidateRequirements("org", "a", []string{"c", "b"}, graph)
	graph[0], graph[2] = graph[2], graph[0]
	second := ValidateRequirements("org", "a", []string{"b", "c", "b"}, graph)
	if first == nil || second == nil || first.Error() != second.Error() {
		t.Fatalf("non-deterministic cycles: %v / %v", first, second)
	}
}
