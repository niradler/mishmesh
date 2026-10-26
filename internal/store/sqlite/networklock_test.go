package sqlite

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/mishmesh/mishmesh/internal/store/storetest"
)

func BenchmarkBeginNetworkWrite(b *testing.B) {
	s := newStore(b)
	ctx := context.Background()
	storetest.CreateTypedOrg(b, s)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		tx, err := s.beginNetworkWrite(ctx)
		if err != nil {
			b.Fatal(err)
		}
		if err := tx.Rollback(); err != nil {
			b.Fatal(err)
		}
	}
}

func TestNetworkWriteExcludesOtherConnectionsEvenWithoutOrgs(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lock.db")
	first, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = first.Close() })
	second, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = second.Close() })

	held, err := first.beginNetworkWrite(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer held.Rollback()

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	blocked, err := second.beginNetworkWrite(ctx)
	if err == nil {
		blocked.Rollback()
		t.Fatal("second network write acquired the lock while the first still held it")
	}
	if !errors.Is(err, context.DeadlineExceeded) && ctx.Err() == nil {
		t.Logf("blocked with: %v", err)
	}
}
