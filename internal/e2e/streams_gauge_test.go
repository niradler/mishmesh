package e2e

import (
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestStreamsActiveGaugeReturnsToZero(t *testing.T) {
	s := startStack(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "ok")
	}), stackOptions{})

	for i := 0; i < 5; i++ {
		resp, err := http.DefaultClient.Do(s.request(http.MethodGet, "demo.localhost", "/", nil))
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(s.scrape(t), `mishmesh_streams_active{kind="http"} 0`) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("streams_active did not return to 0:\n%s", s.scrape(t))
}
