package e2e

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"

	"github.com/mishmesh/mishmesh/internal/authz"
	"github.com/mishmesh/mishmesh/internal/connect/flows"
	"github.com/mishmesh/mishmesh/internal/identity"
	"github.com/mishmesh/mishmesh/internal/store/redis"
	"github.com/mishmesh/mishmesh/internal/token"
)

func clusterIdentityNode(t *testing.T, redisAddr string) *identity.Service {
	t.Helper()
	cs, err := redis.NewConnStore("redis://"+redisAddr, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	keys, err := token.NewKeyring(bytes.Repeat([]byte{9}, 32))
	if err != nil {
		t.Fatal(err)
	}
	svc := identity.New(keys, cs.Sessions())
	stop, err := svc.AttachBus(cs, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(stop)
	return svc
}

func TestRevocationOnOneNodeClosesFlowOnAnother(t *testing.T) {
	mr := miniredis.RunT(t)
	nodeA := clusterIdentityNode(t, mr.Addr())
	nodeB := clusterIdentityNode(t, mr.Addr())
	issued, err := nodeA.Issue(context.Background(), identity.IssueRequest{
		OrgID: "org_1", Subject: authz.EntityRef{Type: authz.EntityServiceAccount, ID: "sa_1"},
		Type: token.TypeAccess, AuthMethod: authz.AuthMethodClientCredentials, TTL: time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	p, err := nodeB.Authenticate(context.Background(), issued.Token, token.TypeAccess)
	if err != nil {
		t.Fatalf("token from node A rejected on node B: %v", err)
	}
	tracker := flows.NewTracker(slog.New(slog.NewTextHandler(io.Discard, nil)), "test", nodeB)
	near, far := net.Pipe()
	defer far.Close()
	_, untrack := tracker.Track(p.OrgID, p.SessionID, near, nil)
	defer untrack()

	if err := nodeA.RevokeSession(context.Background(), p.SessionID); err != nil {
		t.Fatal(err)
	}
	_ = far.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := far.Read(make([]byte, 1)); !errors.Is(err, io.EOF) {
		t.Fatalf("flow on node B still open after revocation on node A: %v", err)
	}
	if _, err := nodeB.Authenticate(context.Background(), issued.Token, token.TypeAccess); !errors.Is(err, identity.ErrUnauthenticated) {
		t.Fatalf("revoked token on node B = %v, want unauthenticated", err)
	}
}

func TestPolicyFlushOnOneNodeReevaluatesFlowsOnAnother(t *testing.T) {
	mr := miniredis.RunT(t)
	nodeA := clusterIdentityNode(t, mr.Addr())
	nodeB := clusterIdentityNode(t, mr.Addr())
	tracker := flows.NewTracker(slog.New(slog.NewTextHandler(io.Discard, nil)), "test", nodeB)
	var denied atomic.Bool
	near, far := net.Pipe()
	defer far.Close()
	_, untrack := tracker.Track("org_1", "", near, func(context.Context) error {
		if denied.Load() {
			return errors.New("policy now denies")
		}
		return nil
	})
	defer untrack()

	denied.Store(true)
	nodeA.FlushAll()
	_ = far.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := far.Read(make([]byte, 1)); !errors.Is(err, io.EOF) {
		t.Fatalf("flow on node B still open after a policy flush on node A: %v", err)
	}
}
