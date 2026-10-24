package e2e

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"

	"github.com/mishmesh/mishmesh/internal/orgsecret"
	"github.com/mishmesh/mishmesh/internal/secrets"
	"github.com/mishmesh/mishmesh/internal/store"
	"github.com/mishmesh/mishmesh/internal/store/redis"
	"github.com/mishmesh/mishmesh/internal/store/sqlite"
)

func clusterVaultNode(t *testing.T, redisAddr string, data store.DataStore) *orgsecret.Vault {
	t.Helper()
	cs, err := redis.NewConnStore("redis://"+redisAddr, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	box, err := secrets.New(bytes.Repeat([]byte{7}, 32))
	if err != nil {
		t.Fatal(err)
	}
	v := orgsecret.New(data, box)
	stop, err := v.AttachBus(cs, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(stop)
	return v
}

func TestSecretRotationOnOneNodeInvalidatesCacheOnAnother(t *testing.T) {
	mr := miniredis.RunT(t)
	data, err := sqlite.Open(filepath.Join(t.TempDir(), "secrets.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = data.Close() })
	ctx := context.Background()
	if err := data.CreateOrg(ctx, &store.Org{ID: "org_1", Name: "o", CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	nodeA := clusterVaultNode(t, mr.Addr(), data)
	nodeB := clusterVaultNode(t, mr.Addr(), data)
	if _, err := nodeA.Put(ctx, "org_1", "api", []byte("one")); err != nil {
		t.Fatal(err)
	}
	if got, err := nodeB.Open(ctx, "org_1", "api"); err != nil || string(got) != "one" {
		t.Fatalf("node B open = %q, %v", got, err)
	}
	if _, err := nodeA.Put(ctx, "org_1", "api", []byte("two")); err != nil {
		t.Fatal(err)
	}
	ztEventually(t, 5*time.Second, "node B serving the rotated secret", func() bool {
		got, err := nodeB.Open(ctx, "org_1", "api")
		return err == nil && string(got) == "two"
	})
	if err := nodeA.Delete(ctx, "org_1", "api"); err != nil {
		t.Fatal(err)
	}
	ztEventually(t, 5*time.Second, "node B refusing the deleted secret", func() bool {
		_, err := nodeB.Open(ctx, "org_1", "api")
		return errors.Is(err, store.ErrNotFound)
	})
}
