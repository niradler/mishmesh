package redis

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	goredis "github.com/redis/go-redis/v9"

	"github.com/mishmesh/mishmesh/internal/store"
)

type afterCommandHook struct {
	command string
	fire    func()
	fired   bool
}

func (h *afterCommandHook) DialHook(next goredis.DialHook) goredis.DialHook { return next }

func (h *afterCommandHook) ProcessHook(next goredis.ProcessHook) goredis.ProcessHook {
	return func(ctx context.Context, cmd goredis.Cmder) error {
		err := next(ctx, cmd)
		if !h.fired && strings.EqualFold(cmd.Name(), h.command) {
			h.fired = true
			h.fire()
		}
		return err
	}
}

func (h *afterCommandHook) ProcessPipelineHook(next goredis.ProcessPipelineHook) goredis.ProcessPipelineHook {
	return next
}

func TestDeleteSubjectSessionsShouldNotOrphanSessionCreatedDuringDelete(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := goredis.NewClient(&goredis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	sessions := newWithClient(rdb).Sessions().(*SessionStore)
	ctx := context.Background()

	session := func(id string) *store.TokenSession {
		return &store.TokenSession{
			ID: id, OrgID: "org1", SubjectType: "user", SubjectID: "u1", TokenID: "t-" + id,
			CreatedAt: time.Now(), ExpiresAt: time.Now().Add(time.Hour),
		}
	}
	if err := sessions.PutTokenSession(ctx, session("old")); err != nil {
		t.Fatal(err)
	}

	rdb.AddHook(&afterCommandHook{command: "smembers", fire: func() {
		if err := sessions.PutTokenSession(ctx, session("late")); err != nil {
			t.Error(err)
		}
	}})

	deleted, err := sessions.DeleteSubjectSessions(ctx, "org1", "user", "u1")
	if err != nil {
		t.Fatal(err)
	}
	removed := map[string]bool{}
	for _, id := range deleted {
		removed[id] = true
	}

	subjectIndex := subjectKeyPrefix + store.SubjectKey("org1", "user", "u1")
	for _, id := range []string{"old", "late"} {
		if removed[id] {
			continue
		}
		if !mr.Exists(sessionKeyPrefix + id) {
			continue
		}
		indexed, err := mr.SIsMember(subjectIndex, id)
		if err != nil || !indexed {
			t.Fatalf("session %q survived the delete but is no longer in the subject index, so it can never be revoked", id)
		}
	}
}
