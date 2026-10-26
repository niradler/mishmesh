package agent

import (
	"crypto/tls"
	"io"
	"log/slog"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func mintingState(tb testing.TB, now time.Time) *decryptState {
	tb.Helper()
	d := newDecryptor(nil, slog.New(slog.NewTextHandler(io.Discard, nil)), func() time.Time { return now })
	key := mustKey(tb)
	st, err := d.stateFrom(key, decryptCertResponse(tb, now, decryptChainSpec{
		interKey: key, permitted: []string{"decrypt.test"}, isCA: true, notAfter: now.Add(time.Hour), allow: []string{"*.decrypt.test"},
	}))
	if err != nil {
		tb.Fatal(err)
	}
	return st
}

func BenchmarkLeafDistinctHostsParallel(b *testing.B) {
	now := time.Now()
	st := mintingState(b, now)
	var n atomic.Int64
	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			host := strconv.FormatInt(n.Add(1), 10) + ".decrypt.test"
			if _, err := st.leaf(host, now); err != nil {
				b.Error(err)
				return
			}
		}
	})
}

func TestLeafMintsOncePerHostUnderContention(t *testing.T) {
	now := time.Now()
	st := mintingState(t, now)
	const callers = 32
	got := make([]*decryptLeafResult, callers)
	var wg sync.WaitGroup
	for i := range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c, err := st.leaf("api.decrypt.test", now)
			got[i] = &decryptLeafResult{cert: c, err: err}
		}()
	}
	wg.Wait()
	for i, r := range got {
		if r.err != nil {
			t.Fatalf("caller %d: %v", i, r.err)
		}
		if r.cert != got[0].cert {
			t.Fatalf("caller %d minted a duplicate leaf", i)
		}
	}
}

func TestDecryptStateNilReceiversFailClosed(t *testing.T) {
	var st *decryptState
	if st.usable(time.Now()) {
		t.Fatal("nil state reported usable")
	}
	if _, err := st.leaf("api.decrypt.test", time.Now()); err == nil {
		t.Fatal("nil state minted a leaf")
	}
}

type decryptLeafResult struct {
	cert *tls.Certificate
	err  error
}
