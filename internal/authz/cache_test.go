package authz

import (
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
