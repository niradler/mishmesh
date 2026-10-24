package flows

import (
	"io"
	"log/slog"
	"net"
	"testing"

	"github.com/mishmesh/mishmesh/internal/identity"
)

func quietTracker() *Tracker {
	return NewTracker(slog.New(slog.NewTextHandler(io.Discard, nil)), "test", nil)
}

func TestRevokeClosesOnlyMatchingFlows(t *testing.T) {
	tr := quietTracker()
	hitNear, hitFar := net.Pipe()
	defer hitFar.Close()
	missNear, missFar := net.Pipe()
	defer missNear.Close()
	defer missFar.Close()
	_, untrackHit := tr.Track("org_a", "ses_1", hitNear)
	defer untrackHit()
	_, untrackMiss := tr.Track("org_b", "ses_1", missNear)
	defer untrackMiss()

	tr.Revoke(identity.Revocation{OrgID: "org_a", SessionIDs: []string{"ses_1"}})
	if _, err := hitFar.Write([]byte("x")); err == nil {
		t.Fatal("revoked flow still open")
	}
	go func() { _, _ = missNear.Read(make([]byte, 1)) }()
	if _, err := missFar.Write([]byte("x")); err != nil {
		t.Fatalf("other org's flow closed: %v", err)
	}
}

func TestRevokedFlowNeverAttaches(t *testing.T) {
	tr := quietTracker()
	near, far := net.Pipe()
	defer far.Close()
	f, untrack := tr.Track("org_a", "ses_1", near)
	tr.Revoke(identity.Revocation{OrgID: "org_a", SessionIDs: []string{"ses_1"}})
	extraNear, extraFar := net.Pipe()
	defer extraNear.Close()
	defer extraFar.Close()
	if tr.Attach(f, extraNear) {
		t.Fatal("attach succeeded on a revoked flow")
	}
	untrack()
	if tr.Len() != 0 {
		t.Fatalf("len = %d after untrack", tr.Len())
	}
}
