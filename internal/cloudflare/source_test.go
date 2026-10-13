package cloudflare

import (
	"context"
	"reflect"
	"testing"

	"github.com/mishmesh/mishmesh/internal/store"
)

type endpointList struct{ endpoints []*store.Endpoint }

func (s endpointList) ListEndpointsByOrg(context.Context, string) ([]*store.Endpoint, error) {
	return s.endpoints, nil
}

func TestShouldSelectPublicHostsPerConnection(t *testing.T) {
	endpoints := []*store.Endpoint{
		nil,
		{ID: "ep", OrgID: "org", Domain: "Notes.Example.Test.", Policy: &store.EndpointPolicy{Access: []store.AccessRef{{ConnectionID: "conn-a"}, {ConnectionID: "conn-b", Hostname: " Alias.Example.Test. "}}}},
		{ID: "off", OrgID: "org", Domain: "off.example.test"},
	}
	for _, test := range []struct {
		connection string
		want       []Hostname
	}{
		{"conn-a", []Hostname{{Host: "notes.example.test", EndpointID: "ep", OrgID: "org", Public: true}}},
		{"conn-b", []Hostname{{Host: "alias.example.test", EndpointID: "ep", OrgID: "org", Public: true}}},
		{"conn-c", []Hostname{}},
	} {
		t.Run(test.connection, func(t *testing.T) {
			if got := AccessHosts(test.connection, endpoints); !reflect.DeepEqual(got, test.want) {
				t.Fatalf("selected=%+v want=%+v", got, test.want)
			}
		})
	}
}

func TestShouldObserveFullOrgWithoutSelectingForeignConnections(t *testing.T) {
	source := StoreSource{ConnectionID: "a", Data: endpointList{[]*store.Endpoint{
		{ID: "selected", OrgID: "org", Domain: "domain.example.test", Policy: &store.EndpointPolicy{Access: []store.AccessRef{{ConnectionID: "a", Hostname: "selected.example.test"}}}},
		{ID: "foreign", OrgID: "org", Domain: "foreign.example.test", Policy: &store.EndpointPolicy{Access: []store.AccessRef{{ConnectionID: "b", Hostname: "alias.example.test"}}}},
		{ID: "off", OrgID: "org", Domain: "off.example.test"},
		{ID: "other-org", OrgID: "other", Domain: "other.example.test", Policy: &store.EndpointPolicy{Access: []store.AccessRef{{ConnectionID: "a"}}}},
	}}}
	hosts, err := source.Hostnames(context.Background(), "org")
	if err != nil {
		t.Fatal(err)
	}
	want := []Hostname{
		{Host: "alias.example.test", EndpointID: "foreign", OrgID: "org"},
		{Host: "domain.example.test", EndpointID: "selected", OrgID: "org"},
		{Host: "foreign.example.test", EndpointID: "foreign", OrgID: "org"},
		{Host: "off.example.test", EndpointID: "off", OrgID: "org"},
		{Host: "selected.example.test", EndpointID: "selected", OrgID: "org", Public: true},
	}
	if !reflect.DeepEqual(hosts, want) {
		t.Fatalf("observer=%+v want=%+v", hosts, want)
	}
}
