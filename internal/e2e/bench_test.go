package e2e

import (
	"bytes"
	"io"
	"net/http"
	"testing"
)

func benchmarkTunnel(b *testing.B, respSize int) {
	payload := bytes.Repeat([]byte("x"), respSize)
	s := startStack(b, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(payload)
	}), stackOptions{})
	client := &http.Client{Transport: &http.Transport{MaxIdleConnsPerHost: 256}}
	b.SetBytes(int64(respSize))
	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			resp, err := client.Do(s.request(http.MethodGet, "demo.localhost", "/", nil))
			if err != nil {
				b.Error(err)
				return
			}
			n, _ := io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
			if int(n) != respSize {
				b.Errorf("got %d bytes", n)
				return
			}
		}
	})
}

func BenchmarkTunnelSmall(b *testing.B) { benchmarkTunnel(b, 256) }

func BenchmarkTunnelLarge(b *testing.B) { benchmarkTunnel(b, 256*1024) }
