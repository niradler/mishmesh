package e2e

import (
	"bytes"
	"io"
	"net/http"
	"testing"
)

func TestBandwidthQuotaEnforcedDuringResponse(t *testing.T) {
	const limit = 200 << 10
	s := startStack(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		chunk := bytes.Repeat([]byte("d"), 32<<10)
		for i := 0; i < 50<<20/len(chunk); i++ {
			if _, err := w.Write(chunk); err != nil {
				return
			}
		}
	}), stackOptions{maxBandwidth: limit})

	resp, err := http.DefaultClient.Do(s.request(http.MethodGet, "demo.localhost", "/big", nil))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	n, _ := io.Copy(io.Discard, resp.Body)
	if n > 4*limit {
		t.Fatalf("delivered %d bytes against a %d byte quota", n, limit)
	}
	if usage := s.conns.Usage(s.orgID); usage > 4*limit {
		t.Fatalf("metered usage %d far beyond quota %d", usage, limit)
	}
}
