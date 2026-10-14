package authz

import (
	"strings"
	"testing"
)

func TestShouldMigrateOnlyParsedLegacyAuthorizationActionEntities(t *testing.T) {
	source := `@note("cloudflare:manage") permit(principal in Role::"member",action in [Action::"cloudflare:manage",Action::"endpoint:read"],resource) when { principal.id != "cloudflare:manage" }; forbid(principal in Role::"admin",action == Action::"cloudflare:manage",resource);`
	migrated, err := MigrateLegacyPolicy(source)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(migrated, `Action::"connection:manage"`) || strings.Contains(migrated, `Action::"cloudflare:manage"`) || !strings.Contains(migrated, `@note("cloudflare:manage")`) || !strings.Contains(migrated, `"cloudflare:manage"`) {
		t.Fatalf("non-action string changed %s", migrated)
	}
	old, err := New([]byte(source))
	if err != nil {
		t.Fatal(err)
	}
	current, err := New([]byte(migrated))
	if err != nil {
		t.Fatal(err)
	}
	for _, role := range []string{RoleOwner, RoleAdmin, RoleMember} {
		p := Principal{ID: "user", Role: role, Org: "org"}
		if old.Authorize(p, Action("cloudflare:manage")) != current.Authorize(p, ActionConnectionManage) {
			t.Fatalf("permission widened for %s", role)
		}
		if old.Authorize(p, ActionEndpointRead) != current.Authorize(p, ActionEndpointRead) {
			t.Fatalf("unrelated action changed for %s", role)
		}
	}
	if reopened, err := MigrateLegacyPolicy(migrated); err != nil || reopened != migrated {
		t.Fatalf("non-idempotent conversion %s %v", reopened, err)
	}
	if _, err := MigrateLegacyPolicy(`permit (malformed)`); err == nil {
		t.Fatal("invalid policy accepted")
	}
}
