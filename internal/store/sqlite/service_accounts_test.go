package sqlite

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/mishmesh/mishmesh/internal/store"
	"github.com/mishmesh/mishmesh/internal/store/storetest"
)

func TestServiceAccounts(t *testing.T) {
	storetest.RunServiceAccounts(t, func(t *testing.T) store.DataStore { return newStore(t) })
}

func TestSignupClaimingInvite(t *testing.T) {
	storetest.RunSignupClaimingInvite(t, func(t *testing.T) store.DataStore { return newStore(t) })
}

func TestClaimInviteCreatingMembership(t *testing.T) {
	storetest.RunClaimInviteCreatingMembership(t, func(t *testing.T) store.DataStore { return newStore(t) })
}

func TestListAllEndpoints(t *testing.T) {
	storetest.RunListAllEndpoints(t, func(t *testing.T) store.DataStore { return newStore(t) })
}

func TestGetUsersByIDs(t *testing.T) {
	storetest.RunGetUsersByIDs(t, func(t *testing.T) store.DataStore { return newStore(t) })
}

func TestKeepOwner(t *testing.T) {
	storetest.RunKeepOwner(t, func(t *testing.T) store.DataStore { return newStore(t) })
}

func TestMembershipDisable(t *testing.T) {
	storetest.RunMembershipDisable(t, func(t *testing.T) store.DataStore { return newStore(t) })
}

func TestMigrateAddsZeroTrustColumns(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	legacy := `
CREATE TABLE orgs (id TEXT PRIMARY KEY, name TEXT NOT NULL, created_at INTEGER NOT NULL);
CREATE TABLE users (id TEXT PRIMARY KEY, email TEXT NOT NULL UNIQUE, name TEXT NOT NULL DEFAULT '', password_hash TEXT, google_sub TEXT, created_at INTEGER NOT NULL);
CREATE TABLE memberships (org_id TEXT NOT NULL, user_id TEXT NOT NULL, role TEXT NOT NULL, created_at INTEGER NOT NULL, PRIMARY KEY (org_id, user_id));
CREATE TABLE sessions (id_hash TEXT PRIMARY KEY, user_id TEXT NOT NULL, org_id TEXT NOT NULL, created_at INTEGER NOT NULL, expires_at INTEGER NOT NULL);
INSERT INTO orgs VALUES ('org_1', 'o', 1);
INSERT INTO users (id, email, created_at) VALUES ('usr_1', 'u@example.com', 1);
INSERT INTO memberships VALUES ('org_1', 'usr_1', 'member', 1);
INSERT INTO sessions VALUES ('sh_1', 'usr_1', 'org_1', 1, 9999999999999999);
`
	if _, err := db.Exec(legacy); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	for range 2 {
		data, err := Open(path)
		if err != nil {
			t.Fatal(err)
		}
		ctx := context.Background()
		m, err := data.GetMembership(ctx, "org_1", "usr_1")
		if err != nil || m.DisabledAt != nil {
			t.Fatalf("membership = %+v %v", m, err)
		}
		s, err := data.GetSession(ctx, "sh_1")
		if err != nil || s.AuthMethod != "" {
			t.Fatalf("session = %+v %v", s, err)
		}
		_ = data.Close()
	}
}

func TestAccessLog(t *testing.T) {
	storetest.RunAccessLog(t, func(t *testing.T) store.DataStore { return newStore(t) })
}
