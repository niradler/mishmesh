package agent

import (
	"os"
	"strings"
	"testing"
)

func writeFile(path string) error {
	return os.WriteFile(path, []byte("tunnels: {}"), 0o600)
}

func TestNormalizeGatewayURL(t *testing.T) {
	tests := []struct {
		in      string
		want    string
		wantErr string
	}{
		{in: "ws://localhost:8081", want: "ws://localhost:8081"},
		{in: "wss://connect.example.com", want: "wss://connect.example.com"},
		{in: "http://localhost:8081", want: "ws://localhost:8081"},
		{in: "https://connect.example.com/", want: "wss://connect.example.com"},
		{in: "HTTPS://Connect.Example.com", want: "wss://Connect.Example.com"},
		{in: "  wss://x.io  ", want: "wss://x.io"},
		{in: "wss://x.io/base/", want: "wss://x.io/base"},
		{in: "", wantErr: "gateway URL is empty"},
		{in: "localhost:8081", wantErr: "unsupported scheme"},
		{in: "connect.example.com", wantErr: "missing scheme"},
		{in: "ftp://x.io", wantErr: "unsupported scheme"},
		{in: "ws://", wantErr: "missing host"},
		{in: "ws://bad host", wantErr: "invalid gateway URL"},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			got, err := NormalizeGatewayURL(tt.in)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want containing %q", err, tt.wantErr)
				}
				return
			}
			if err != nil || got != tt.want {
				t.Fatalf("got %q, %v, want %q", got, err, tt.want)
			}
		})
	}
}
