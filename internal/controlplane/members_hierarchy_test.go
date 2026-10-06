package controlplane

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

type hierarchyFixture struct {
	srv                        *httptest.Server
	owner                      *http.Client
	admin                      *http.Client
	member                     *http.Client
	ownerID, adminID, memberID string
}

func newHierarchyFixture(t *testing.T) *hierarchyFixture {
	t.Helper()
	srv := newModeAPI(t, "org")
	owner, ownerMe := register(t, srv, "owner@example.com", http.StatusCreated)
	f := &hierarchyFixture{srv: srv, owner: owner, ownerID: ownerMe.ID}
	for _, who := range []struct {
		email, role string
		client      **http.Client
		id          *string
	}{
		{"admin@example.com", "admin", &f.admin, &f.adminID},
		{"member@example.com", "member", &f.member, &f.memberID},
	} {
		inv := invite(t, owner, srv, who.email, who.role)
		c, me := registerTok(t, srv, who.email, inv.InviteToken, http.StatusCreated)
		doc(t, c, srv, http.MethodPost, "/api/v1/auth/switch-org", `{"org_id":"`+ownerMe.ActiveOrgID+`"}`, http.StatusOK, nil)
		*who.client = c
		*who.id = me.ID
	}
	return f
}

func TestMemberHierarchyEnforced(t *testing.T) {
	f := newHierarchyFixture(t)

	tests := []struct {
		name   string
		client func() *http.Client
		method string
		path   string
		body   string
		want   int
	}{
		{"admin cannot demote owner", func() *http.Client { return f.admin }, http.MethodPatch, "/api/v1/members/" + f.ownerID, `{"role":"member"}`, http.StatusForbidden},
		{"admin cannot remove owner", func() *http.Client { return f.admin }, http.MethodDelete, "/api/v1/members/" + f.ownerID, "", http.StatusForbidden},
		{"admin cannot promote self to owner", func() *http.Client { return f.admin }, http.MethodPatch, "/api/v1/members/" + f.adminID, `{"role":"owner"}`, http.StatusForbidden},
		{"admin cannot write policy", func() *http.Client { return f.admin }, http.MethodPut, "/api/v1/policy", `{"matrix":{"owner":["policy:write"]}}`, http.StatusForbidden},
		{"owner can write policy", func() *http.Client { return f.owner }, http.MethodPut, "/api/v1/policy", `{"matrix":{"owner":["agent:read","agent:write","member:read","member:manage","policy:read","policy:write","endpoint:read","endpoint:write","quota:read","audit:read","status:read"],"admin":["member:manage","member:read"],"member":["agent:read"]}}`, http.StatusOK},
		{"sole owner cannot demote self", func() *http.Client { return f.owner }, http.MethodPatch, "/api/v1/members/" + f.ownerID, `{"role":"admin"}`, http.StatusConflict},
		{"sole owner cannot remove self", func() *http.Client { return f.owner }, http.MethodDelete, "/api/v1/members/" + f.ownerID, "", http.StatusConflict},
		{"admin can demote member peer below", func() *http.Client { return f.admin }, http.MethodPatch, "/api/v1/members/" + f.memberID, `{"role":"member"}`, http.StatusOK},
		{"admin can remove member", func() *http.Client { return f.admin }, http.MethodDelete, "/api/v1/members/" + f.memberID, "", http.StatusNoContent},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			doc(t, tc.client(), f.srv, tc.method, tc.path, tc.body, tc.want, nil)
		})
	}
}

func TestLastOwnerGuardReleasedWithSecondOwner(t *testing.T) {
	f := newHierarchyFixture(t)
	doc(t, f.owner, f.srv, http.MethodPatch, "/api/v1/members/"+f.adminID, `{"role":"owner"}`, http.StatusOK, nil)
	doc(t, f.owner, f.srv, http.MethodPatch, "/api/v1/members/"+f.ownerID, `{"role":"admin"}`, http.StatusOK, nil)
	doc(t, f.admin, f.srv, http.MethodPatch, "/api/v1/members/"+f.adminID, `{"role":"admin"}`, http.StatusConflict, nil)
}
