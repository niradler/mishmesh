package controlplane

import (
	"net/http"
	"strings"
	"testing"
)

func TestGroupsCRUDAndMembership(t *testing.T) {
	f := newHierarchyFixture(t)
	var g groupDTO
	doc(t, f.owner, f.srv, http.MethodPost, "/api/v1/groups", `{"name":"ops team"}`, http.StatusCreated, &g)
	if g.ID == "" || g.Name != "ops team" {
		t.Fatalf("created group = %+v", g)
	}
	doc(t, f.owner, f.srv, http.MethodPost, "/api/v1/groups/"+g.ID+"/members", `{"type":"user","id":"`+f.memberID+`"}`, http.StatusNoContent, nil)
	doc(t, f.owner, f.srv, http.MethodPost, "/api/v1/groups/"+g.ID+"/members", `{"type":"user","id":"`+f.memberID+`"}`, http.StatusNoContent, nil)

	var got groupDTO
	doc(t, f.member, f.srv, http.MethodGet, "/api/v1/groups/"+g.ID, "", http.StatusOK, &got)
	if len(got.Members) != 1 || got.Members[0].ID != f.memberID || got.Members[0].Type != "user" {
		t.Fatalf("members = %+v", got.Members)
	}

	doc(t, f.owner, f.srv, http.MethodPatch, "/api/v1/groups/"+g.ID, `{"name":"sre"}`, http.StatusOK, &got)
	if got.Name != "sre" {
		t.Fatalf("rename = %+v", got)
	}
	var list []groupDTO
	doc(t, f.member, f.srv, http.MethodGet, "/api/v1/groups", "", http.StatusOK, &list)
	if len(list) != 1 || list[0].Name != "sre" {
		t.Fatalf("list = %+v", list)
	}

	doc(t, f.owner, f.srv, http.MethodDelete, "/api/v1/groups/"+g.ID+"/members/user/"+f.memberID, "", http.StatusNoContent, nil)
	doc(t, f.owner, f.srv, http.MethodDelete, "/api/v1/groups/"+g.ID+"/members/user/"+f.memberID, "", http.StatusNotFound, nil)
	doc(t, f.owner, f.srv, http.MethodDelete, "/api/v1/groups/"+g.ID, "", http.StatusNoContent, nil)
	doc(t, f.owner, f.srv, http.MethodGet, "/api/v1/groups/"+g.ID, "", http.StatusNotFound, nil)

	var audit []auditDTO
	doc(t, f.owner, f.srv, http.MethodGet, "/api/v1/audit", "", http.StatusOK, &audit)
	seen := map[string]bool{}
	for _, e := range audit {
		seen[e.Action] = true
	}
	for _, want := range []string{"group.create", "group.update", "group.delete", "group.member.add", "group.member.remove"} {
		if !seen[want] {
			t.Errorf("missing audit event %q", want)
		}
	}
}

func TestGroupsRejectInvalidInput(t *testing.T) {
	f := newHierarchyFixture(t)
	var g groupDTO
	doc(t, f.owner, f.srv, http.MethodPost, "/api/v1/groups", `{"name":"ops"}`, http.StatusCreated, &g)
	members := "/api/v1/groups/" + g.ID + "/members"
	tests := []struct {
		name   string
		method string
		path   string
		body   string
		want   int
	}{
		{"empty name", http.MethodPost, "/api/v1/groups", `{"name":""}`, http.StatusBadRequest},
		{"name too long", http.MethodPost, "/api/v1/groups", `{"name":"` + strings.Repeat("a", 65) + `"}`, http.StatusBadRequest},
		{"only spaces", http.MethodPost, "/api/v1/groups", `{"name":"   "}`, http.StatusBadRequest},
		{"leading space", http.MethodPost, "/api/v1/groups", `{"name":" ops2"}`, http.StatusBadRequest},
		{"max length", http.MethodPost, "/api/v1/groups", `{"name":"` + strings.Repeat("b", 64) + `"}`, http.StatusCreated},
		{"single char", http.MethodPost, "/api/v1/groups", `{"name":"x"}`, http.StatusCreated},
		{"name bad chars", http.MethodPost, "/api/v1/groups", `{"name":"ops<script>"}`, http.StatusBadRequest},
		{"duplicate name", http.MethodPost, "/api/v1/groups", `{"name":"ops"}`, http.StatusConflict},
		{"rename invalid", http.MethodPatch, "/api/v1/groups/" + g.ID, `{"name":"a/b"}`, http.StatusBadRequest},
		{"unknown member type", http.MethodPost, members, `{"type":"robot","id":"x"}`, http.StatusBadRequest},
		{"service account not supported", http.MethodPost, members, `{"type":"service_account","id":"sa_1"}`, http.StatusBadRequest},
		{"non member user", http.MethodPost, members, `{"type":"user","id":"usr_nope"}`, http.StatusNotFound},
		{"unknown group", http.MethodPost, "/api/v1/groups/grp_nope/members", `{"type":"user","id":"` + f.memberID + `"}`, http.StatusNotFound},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			doc(t, f.owner, f.srv, tc.method, tc.path, tc.body, tc.want, nil)
		})
	}
}

func TestGroupsRequireMemberManage(t *testing.T) {
	f := newHierarchyFixture(t)
	var g groupDTO
	doc(t, f.owner, f.srv, http.MethodPost, "/api/v1/groups", `{"name":"ops"}`, http.StatusCreated, &g)
	tests := []struct {
		name   string
		method string
		path   string
		body   string
	}{
		{"create", http.MethodPost, "/api/v1/groups", `{"name":"x"}`},
		{"rename", http.MethodPatch, "/api/v1/groups/" + g.ID, `{"name":"x"}`},
		{"delete", http.MethodDelete, "/api/v1/groups/" + g.ID, ""},
		{"add member", http.MethodPost, "/api/v1/groups/" + g.ID + "/members", `{"type":"user","id":"` + f.memberID + `"}`},
		{"remove member", http.MethodDelete, "/api/v1/groups/" + g.ID + "/members/user/" + f.memberID, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			doc(t, f.member, f.srv, tc.method, tc.path, tc.body, http.StatusForbidden, nil)
		})
	}
}

func TestGroupsAreIsolatedAcrossOrgs(t *testing.T) {
	f := newHierarchyFixture(t)
	var g groupDTO
	doc(t, f.owner, f.srv, http.MethodPost, "/api/v1/groups", `{"name":"ops"}`, http.StatusCreated, &g)
	doc(t, f.owner, f.srv, http.MethodPost, "/api/v1/groups/"+g.ID+"/members", `{"type":"user","id":"`+f.memberID+`"}`, http.StatusNoContent, nil)

	outsider, outsiderMe := register(t, f.srv, "outsider@example.com", http.StatusCreated)
	doc(t, f.owner, f.srv, http.MethodPost, "/api/v1/groups/"+g.ID+"/members", `{"type":"user","id":"`+outsiderMe.ID+`"}`, http.StatusNotFound, nil)

	tests := []struct {
		name   string
		method string
		path   string
		body   string
	}{
		{"get", http.MethodGet, "/api/v1/groups/" + g.ID, ""},
		{"rename", http.MethodPatch, "/api/v1/groups/" + g.ID, `{"name":"stolen"}`},
		{"delete", http.MethodDelete, "/api/v1/groups/" + g.ID, ""},
		{"add member", http.MethodPost, "/api/v1/groups/" + g.ID + "/members", `{"type":"user","id":"` + outsiderMe.ID + `"}`},
		{"remove member", http.MethodDelete, "/api/v1/groups/" + g.ID + "/members/user/" + f.memberID, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			doc(t, outsider, f.srv, tc.method, tc.path, tc.body, http.StatusNotFound, nil)
		})
	}
	var list []groupDTO
	doc(t, outsider, f.srv, http.MethodGet, "/api/v1/groups", "", http.StatusOK, &list)
	if len(list) != 0 {
		t.Fatalf("outsider sees groups: %+v", list)
	}
	var got groupDTO
	doc(t, f.owner, f.srv, http.MethodGet, "/api/v1/groups/"+g.ID, "", http.StatusOK, &got)
	if got.Name != "ops" || len(got.Members) != 1 {
		t.Fatalf("outsider changed group: %+v", got)
	}
}

func TestRemovingOrgMemberDropsGroupMembership(t *testing.T) {
	f := newHierarchyFixture(t)
	var g groupDTO
	doc(t, f.owner, f.srv, http.MethodPost, "/api/v1/groups", `{"name":"ops"}`, http.StatusCreated, &g)
	doc(t, f.owner, f.srv, http.MethodPost, "/api/v1/groups/"+g.ID+"/members", `{"type":"user","id":"`+f.memberID+`"}`, http.StatusNoContent, nil)
	doc(t, f.owner, f.srv, http.MethodDelete, "/api/v1/members/"+f.memberID, "", http.StatusNoContent, nil)
	var got groupDTO
	doc(t, f.owner, f.srv, http.MethodGet, "/api/v1/groups/"+g.ID, "", http.StatusOK, &got)
	if len(got.Members) != 0 {
		t.Fatalf("removed member still in group: %+v", got.Members)
	}
}
