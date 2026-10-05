package e2e

import (
	"fmt"
	"io"
	"net/http"
	"testing"
)

func TestSubdomainHostBeatsTunnelPath(t *testing.T) {
	s := startStack(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "app saw %s", r.URL.Path)
	}), stackOptions{})

	resp, err := http.DefaultClient.Do(s.request(http.MethodGet, "demo.localhost", "/tunnel/assets/app.js", nil))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK || string(body) != "app saw /tunnel/assets/app.js" {
		t.Fatalf("status=%d body=%q", resp.StatusCode, body)
	}

	resp2, err := http.DefaultClient.Do(s.request(http.MethodGet, "localhost", "/tunnel/"+s.endpoint.ID+"/x", nil))
	if err != nil {
		t.Fatal(err)
	}
	defer resp2.Body.Close()
	body2, _ := io.ReadAll(resp2.Body)
	if string(body2) != "app saw /x" {
		t.Fatalf("base-domain path routing broken: status=%d body=%q", resp2.StatusCode, body2)
	}
}
