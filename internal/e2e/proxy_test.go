package e2e

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestExpectContinueLargeUpload(t *testing.T) {
	s := startStack(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n, _ := io.Copy(io.Discard, r.Body)
		fmt.Fprintf(w, "received %d", n)
	}), stackOptions{})

	size := 3 << 20
	client := &http.Client{Transport: &http.Transport{ExpectContinueTimeout: 2 * time.Second}}
	req := s.request(http.MethodPut, "demo.localhost", "/upload", bytes.NewReader(bytes.Repeat([]byte("u"), size)))
	req.Header.Set("Expect", "100-continue")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if want := fmt.Sprintf("received %d", size); string(body) != want {
		t.Fatalf("status=%d body=%q want %q", resp.StatusCode, body, want)
	}
}

func TestStreamingResponseFlushedIncrementally(t *testing.T) {
	release := make(chan struct{})
	s := startStack(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: first\n\n")
		w.(http.Flusher).Flush()
		select {
		case <-release:
		case <-r.Context().Done():
		}
		fmt.Fprint(w, "data: second\n\n")
	}), stackOptions{})

	defer close(release)
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(s.request(http.MethodGet, "demo.localhost", "/events", nil))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	got := make(chan string, 1)
	go func() {
		buf := make([]byte, 64)
		n, _ := resp.Body.Read(buf)
		got <- string(buf[:n])
	}()
	select {
	case first := <-got:
		if !strings.Contains(first, "first") {
			t.Fatalf("first chunk = %q", first)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("first SSE event was not flushed before the stream completed")
	}
}

func TestForwardedHeaders(t *testing.T) {
	s := startStack(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "xff=%s|proto=%s|host=%s", r.Header.Get("X-Forwarded-For"), r.Header.Get("X-Forwarded-Proto"), r.Header.Get("X-Forwarded-Host"))
	}), stackOptions{})

	req := s.request(http.MethodGet, "demo.localhost", "/", nil)
	req.Header.Set("X-Forwarded-For", "6.6.6.6")
	req.Header.Set("X-Forwarded-Proto", "https")
	req.Header.Set("X-Forwarded-Host", "evil.example")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	want := "xff=127.0.0.1|proto=http|host=demo.localhost"
	if string(body) != want {
		t.Fatalf("got %q want %q", body, want)
	}
}
