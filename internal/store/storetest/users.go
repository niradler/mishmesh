package storetest

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/mishmesh/mishmesh/internal/store"
)

func RunGetUsersByIDs(t *testing.T, open func(t *testing.T) store.DataStore) {
	ctx := context.Background()
	ds := open(t)
	now := time.Now().UTC().Truncate(time.Microsecond)
	var ids []string
	for i := 0; i < 600; i++ {
		id := store.NewID("usr")
		if err := ds.CreateUser(ctx, &store.User{ID: id, Email: id + "@example.com", Name: "n", CreatedAt: now}); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}

	t.Run("returns every existing user across chunks and skips unknown and duplicate ids", func(t *testing.T) {
		query := append(slices.Clone(ids), "usr_missing", ids[0], ids[1])
		got, err := ds.GetUsersByIDs(ctx, query)
		if err != nil {
			t.Fatal(err)
		}
		gotIDs := make([]string, len(got))
		for i, u := range got {
			gotIDs[i] = u.ID
			if u.Email != u.ID+"@example.com" || !u.CreatedAt.Equal(now) {
				t.Fatalf("user = %+v", u)
			}
		}
		slices.Sort(gotIDs)
		want := slices.Clone(ids)
		slices.Sort(want)
		if !slices.Equal(gotIDs, want) {
			t.Fatalf("got %d users, want %d", len(gotIDs), len(want))
		}
	})

	t.Run("empty input", func(t *testing.T) {
		got, err := ds.GetUsersByIDs(ctx, nil)
		if err != nil || len(got) != 0 {
			t.Fatalf("got %v %v", got, err)
		}
	})
}
