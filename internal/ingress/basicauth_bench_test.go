package ingress

import (
	"net/http/httptest"
	"testing"

	"golang.org/x/crypto/bcrypt"
)

func BenchmarkBasicAuthWrongPassword(b *testing.B) {
	hash, err := bcrypt.GenerateFromPassword([]byte("s3cret"), bcrypt.DefaultCost)
	if err != nil {
		b.Fatal(err)
	}
	cache := newTTLCache[struct{}](basicAuthCacheTTL)
	r := httptest.NewRequest("GET", "/", nil)
	r.SetBasicAuth("alice", "wrong")
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		checkBasicAuth(r, "ep_1", "alice", string(hash), cache, nil)
	}
}
