package main

import (
	"io"
	"log/slog"
	"testing"
)

func TestValidatePprofAddr(t *testing.T) {
	tests := []struct {
		addr    string
		wantErr bool
	}{
		{"127.0.0.1:6060", false},
		{"localhost:6060", false},
		{"[::1]:6060", false},
		{"0.0.0.0:6060", true},
		{":6060", true},
		{"10.0.0.5:6060", true},
		{"nonsense", true},
	}
	for _, tt := range tests {
		t.Run(tt.addr, func(t *testing.T) {
			if err := validatePprofAddr(tt.addr); (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestStartPprofServesAndStops(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	stop, err := startPprof("", log)
	if err != nil {
		t.Fatal(err)
	}
	stop()
	if _, err := startPprof("0.0.0.0:0", log); err == nil {
		t.Fatal("expected non-loopback bind to be refused")
	}
	stop, err = startPprof("127.0.0.1:0", log)
	if err != nil {
		t.Fatal(err)
	}
	stop()
}
