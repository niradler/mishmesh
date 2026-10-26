package ingress

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestKeyedLocksSerializeAndEvict(t *testing.T) {
	var locks keyedLocks
	release, err := locks.acquire(context.Background(), "a")
	if err != nil {
		t.Fatal(err)
	}
	other, err := locks.acquire(context.Background(), "b")
	if err != nil {
		t.Fatal("independent key blocked:", err)
	}
	other()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := locks.acquire(ctx, "a"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("second holder of a held key: err = %v, want deadline exceeded", err)
	}

	acquired := make(chan func())
	go func() {
		next, _ := locks.acquire(context.Background(), "a")
		acquired <- next
	}()
	release()
	(<-acquired)()

	if n := locks.size(); n != 0 {
		t.Fatalf("idle keys retained = %d, want 0", n)
	}
}
