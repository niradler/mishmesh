package authz

import (
	"crypto/sha256"
	"fmt"
	"sync"
	"testing"
)

func TestShouldReuseCompiledAuthorizerForSameSource(t *testing.T) {
	c := NewCache(4)
	src := []byte(`permit (principal, action == Action::"http:request", resource);`)
	first, err := c.Get(src)
	if err != nil {
		t.Fatal(err)
	}
	second, err := c.Get(append([]byte{}, src...))
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatal("same source compiled twice")
	}
	other, err := c.Get([]byte(`permit (principal, action == Action::"endpoint:access", resource);`))
	if err != nil {
		t.Fatal(err)
	}
	if other == first {
		t.Fatal("different sources share an authorizer")
	}
}

func TestShouldNotCacheParseErrors(t *testing.T) {
	c := NewCache(4)
	if _, err := c.Get([]byte(`permit (`)); err == nil {
		t.Fatal("want parse error")
	}
	if c.Len() != 0 {
		t.Fatalf("len = %d, want 0", c.Len())
	}
}

func TestShouldBoundCacheSize(t *testing.T) {
	c := NewCache(3)
	for i := range 10 {
		if _, err := c.Get([]byte(fmt.Sprintf(`@id("p%d") permit (principal, action, resource);`, i))); err != nil {
			t.Fatal(err)
		}
		if c.Len() > 3 {
			t.Fatalf("len = %d exceeds max", c.Len())
		}
	}
}

func TestShouldKeepMostEntriesWhenFull(t *testing.T) {
	c := NewCache(8)
	sources := make([][]byte, 0, 8)
	firsts := make([]*Authorizer, 0, 8)
	for i := range 8 {
		src := []byte(fmt.Sprintf(`@id("p%d") permit (principal, action, resource);`, i))
		az, err := c.Get(src)
		if err != nil {
			t.Fatal(err)
		}
		sources = append(sources, src)
		firsts = append(firsts, az)
	}
	if _, err := c.Get([]byte(`@id("extra") permit (principal, action, resource);`)); err != nil {
		t.Fatal(err)
	}
	kept := 0
	for i, src := range sources {
		c.mu.Lock()
		az, ok := c.entries[sha256.Sum256(src)]
		c.mu.Unlock()
		if ok && az == firsts[i] {
			kept++
		}
	}
	if kept < 7 {
		t.Fatalf("%d of 8 cached authorizers survived one extra insert", kept)
	}
}

func TestShouldBeSafeForConcurrentUse(t *testing.T) {
	c := NewCache(8)
	var wg sync.WaitGroup
	for i := range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			src := []byte(fmt.Sprintf(`@id("p%d") permit (principal, action, resource);`, i%4))
			if _, err := c.Get(src); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
}

func TestDenyAllDeniesEveryAction(t *testing.T) {
	az := DenyAll()
	for _, act := range AllActions() {
		if az.Authorize(Principal{ID: "u", Role: RoleOwner, Org: "o"}, act) {
			t.Errorf("deny-all permitted %s", act)
		}
	}
	req := DecisionRequest{
		Principal: EntityRef{Type: EntityAnonymous, ID: AnonymousID},
		Action:    ActionHTTPRequest,
		Resource:  EntityRef{Type: EntityEndpoint, ID: "ep_1"},
		Context:   RequestContext{ClientIP: "203.0.113.7", Host: "a.example.com", Method: "GET", Path: "/", Protocol: "http", AuthMethod: AuthMethodNone},
	}
	if d := az.Decide(req, Facts{Subject: &Subject{Ref: req.Principal}, Endpoint: &EndpointEntity{ID: "ep_1", Kind: "http"}}); d.Allowed || d.Reason != ReasonNoPolicyMatch {
		t.Fatalf("deny-all decision = %+v", d)
	}
}
