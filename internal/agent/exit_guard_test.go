package agent

import (
	"errors"
	"io"
	"log/slog"
	"testing"

	"github.com/mishmesh/mishmesh/internal/connect/proxy"
)

func TestExitGuardHonoursExitAllow(t *testing.T) {
	g, ok := exitGuard([]string{"10.20.0.0/16"}, slog.New(slog.NewTextHandler(io.Discard, nil))).(*proxy.Guard)
	if !ok {
		t.Fatal("exit guard is not a proxy guard")
	}
	tests := []struct {
		target string
		ok     bool
	}{
		{"10.20.4.5:5432", true},
		{"10.30.4.5:5432", false},
		{"127.0.0.1:22", false},
		{"169.254.169.254:80", false},
	}
	for _, tt := range tests {
		err := g.ValidateTarget(tt.target)
		if (err == nil) != tt.ok || (err != nil && !errors.Is(err, proxy.ErrBlocked)) {
			t.Errorf("ValidateTarget(%s) = %v, want ok=%v", tt.target, err, tt.ok)
		}
	}
}

func TestAgentBuildsExitGuardFromConfig(t *testing.T) {
	a := New(Options{Egress: &EgressSpec{ServeExit: true, ExitAllow: []string{"192.168.50.0/24"}}, Log: slog.New(slog.NewTextHandler(io.Discard, nil))})
	g, ok := a.opts.ExitDialer.(*proxy.Guard)
	if !ok || g.ValidateTarget("192.168.50.9:80") != nil || g.ValidateTarget("192.168.51.9:80") == nil {
		t.Fatalf("exit dialer = %T, does not honour exit_allow", a.opts.ExitDialer)
	}
}
