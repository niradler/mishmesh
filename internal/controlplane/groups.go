package controlplane

import (
	"net/http"
	"regexp"
	"time"

	"github.com/mishmesh/mishmesh/internal/store"
)

var groupNamePattern = regexp.MustCompile(`^[A-Za-z0-9._-]([A-Za-z0-9 ._-]{0,62}[A-Za-z0-9._-])?$`)

type groupMemberDTO struct {
	Type      string    `json:"type"`
	ID        string    `json:"id"`
	CreatedAt time.Time `json:"created_at"`
}

type groupDTO struct {
	ID        string           `json:"id"`
	Name      string           `json:"name"`
	CreatedAt time.Time        `json:"created_at"`
	Members   []groupMemberDTO `json:"members,omitempty"`
}

func toGroupDTO(g *store.Group) groupDTO {
	return groupDTO{ID: g.ID, Name: g.Name, CreatedAt: g.CreatedAt}
}

func validGroupName(w http.ResponseWriter, name string) bool {
	if !groupNamePattern.MatchString(name) {
		writeError(w, http.StatusBadRequest, "group name must be 1-64 letters, digits, spaces, '.', '_' or '-'")
		return false
	}
	return true
}

func (a *API) listGroupsHandler(w http.ResponseWriter, r *http.Request) {
	groups, err := a.data.ListGroups(r.Context(), a.orgScope(r))
	writeList(a, w, groups, err, toGroupDTO)
}

func (a *API) createGroupHandler(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"name"`
	}
	if !decodeJSON(w, r, &req) || !validGroupName(w, req.Name) {
		return
	}
	g := &store.Group{ID: store.NewID("grp"), OrgID: a.orgScope(r), Name: req.Name, CreatedAt: time.Now()}
	if a.handleStoreErr(w, a.data.CreateGroup(r.Context(), g), "a group with that name already exists") {
		return
	}
	a.audit(r, "group.create", g.ID, g.Name)
	writeJSON(w, http.StatusCreated, toGroupDTO(g))
}

func (a *API) getGroupHandler(w http.ResponseWriter, r *http.Request) {
	org := a.orgScope(r)
	g, err := a.data.GetGroup(r.Context(), org, r.PathValue("id"))
	if a.handleErr(w, err) {
		return
	}
	members, err := a.data.ListGroupMembers(r.Context(), org, g.ID)
	if a.handleErr(w, err) {
		return
	}
	dto := toGroupDTO(g)
	dto.Members = make([]groupMemberDTO, 0, len(members))
	for _, m := range members {
		dto.Members = append(dto.Members, groupMemberDTO{Type: m.MemberType, ID: m.MemberID, CreatedAt: m.CreatedAt})
	}
	writeJSON(w, http.StatusOK, dto)
}

func (a *API) renameGroupHandler(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"name"`
	}
	if !decodeJSON(w, r, &req) || !validGroupName(w, req.Name) {
		return
	}
	org := a.orgScope(r)
	id := r.PathValue("id")
	if a.handleStoreErr(w, a.data.RenameGroup(r.Context(), org, id, req.Name), "a group with that name already exists") {
		return
	}
	g, err := a.data.GetGroup(r.Context(), org, id)
	if a.handleErr(w, err) {
		return
	}
	a.audit(r, "group.update", g.ID, g.Name)
	writeJSON(w, http.StatusOK, toGroupDTO(g))
}

func (a *API) deleteGroupHandler(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if a.handleErr(w, a.data.DeleteGroup(r.Context(), a.orgScope(r), id)) {
		return
	}
	a.flushDecisions(a.orgScope(r))
	a.audit(r, "group.delete", id, "")
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) addGroupMemberHandler(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Type string `json:"type"`
		ID   string `json:"id"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	org := a.orgScope(r)
	groupID := r.PathValue("id")
	switch req.Type {
	case store.GroupMemberUser:
		if _, err := a.data.GetMembership(r.Context(), org, req.ID); a.handleErr(w, err) {
			return
		}
	case store.GroupMemberServiceAccount:
		if _, err := a.data.GetServiceAccount(r.Context(), org, req.ID); a.handleErr(w, err) {
			return
		}
	default:
		writeError(w, http.StatusBadRequest, "member type must be user or service_account")
		return
	}
	m := &store.GroupMember{OrgID: org, GroupID: groupID, MemberType: req.Type, MemberID: req.ID, CreatedAt: time.Now()}
	if a.handleErr(w, a.data.AddGroupMember(r.Context(), m)) {
		return
	}
	a.flushDecisions(org)
	a.audit(r, "group.member.add", groupID, req.Type+":"+req.ID)
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) removeGroupMemberHandler(w http.ResponseWriter, r *http.Request) {
	groupID := r.PathValue("id")
	memberType := r.PathValue("type")
	memberID := r.PathValue("member_id")
	if a.handleErr(w, a.data.RemoveGroupMember(r.Context(), a.orgScope(r), groupID, memberType, memberID)) {
		return
	}
	a.flushDecisions(a.orgScope(r))
	a.audit(r, "group.member.remove", groupID, memberType+":"+memberID)
	w.WriteHeader(http.StatusNoContent)
}
