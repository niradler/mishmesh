package controlplane

import (
	"bufio"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
)

func TestReachInHTTPTruncatedResponseIsBadGateway(t *testing.T) {
	f := newStreamFixture(t, "", true)
	f.agent.scripted = func(c net.Conn) {
		br := bufio.NewReader(c)
		for {
			line, err := br.ReadString('\n')
			if err != nil || line == "\r\n" {
				break
			}
		}
		_, _ = io.WriteString(c, "HTTP/1.1 200 OK\r\nContent-Length: 100\r\n\r\npartial")
	}
	resp, err := http.Post(f.srv.URL+"/api/v1/reach/ag_a/http?org_id=org_a", "application/json", strings.NewReader(`{"target":"x:1","path":"/"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("truncated upstream body was returned as success: status=%d body=%s", resp.StatusCode, body)
	}
}
