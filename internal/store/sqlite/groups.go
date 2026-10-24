package sqlite

import (
	"context"

	"github.com/mishmesh/mishmesh/internal/store"
)

const groupsSchema = `
CREATE TABLE IF NOT EXISTS groups (
  id         TEXT PRIMARY KEY,
  org_id     TEXT NOT NULL REFERENCES orgs(id) ON DELETE CASCADE,
  name       TEXT NOT NULL,
  created_at INTEGER NOT NULL,
  UNIQUE (org_id, name)
);
CREATE TABLE IF NOT EXISTS group_members (
  org_id      TEXT NOT NULL,
  group_id    TEXT NOT NULL REFERENCES groups(id) ON DELETE CASCADE,
  member_type TEXT NOT NULL,
  member_id   TEXT NOT NULL,
  created_at  INTEGER NOT NULL,
  PRIMARY KEY (group_id, member_type, member_id)
);
CREATE INDEX IF NOT EXISTS idx_group_members_member ON group_members(org_id, member_type, member_id);
`

const groupCols = `id, org_id, name, created_at`

func (s *Store) CreateGroup(ctx context.Context, g *store.Group) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO groups (`+groupCols+`) VALUES (?, ?, ?, ?)`, g.ID, g.OrgID, g.Name, ns(g.CreatedAt))
	return uniqueErr("create group", err)
}

func (s *Store) GetGroup(ctx context.Context, orgID, id string) (*store.Group, error) {
	return scanGroup(s.db.QueryRowContext(ctx, `SELECT `+groupCols+` FROM groups WHERE org_id = ? AND id = ?`, orgID, id).Scan)
}

func (s *Store) ListGroups(ctx context.Context, orgID string) ([]*store.Group, error) {
	return s.queryGroups(ctx, `SELECT `+groupCols+` FROM groups WHERE org_id = ? ORDER BY name, id`, orgID)
}

func (s *Store) RenameGroup(ctx context.Context, orgID, id, name string) error {
	res, err := s.db.ExecContext(ctx, `UPDATE groups SET name = ? WHERE org_id = ? AND id = ?`, name, orgID, id)
	if err != nil {
		return uniqueErr("rename group", err)
	}
	return affected("rename group", res)
}

func (s *Store) DeleteGroup(ctx context.Context, orgID, id string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM groups WHERE org_id = ? AND id = ?`, orgID, id)
	if err != nil {
		return wrap("delete group", err)
	}
	return affected("delete group", res)
}

func (s *Store) AddGroupMember(ctx context.Context, m *store.GroupMember) error {
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO group_members (org_id, group_id, member_type, member_id, created_at)
		 SELECT org_id, id, ?, ?, ? FROM groups WHERE org_id = ? AND id = ?
		 ON CONFLICT (group_id, member_type, member_id) DO NOTHING`,
		m.MemberType, m.MemberID, ns(m.CreatedAt), m.OrgID, m.GroupID)
	if err != nil {
		return wrap("add group member", err)
	}
	if n, err := res.RowsAffected(); err != nil || n > 0 {
		return wrap("add group member", err)
	}
	_, err = s.GetGroup(ctx, m.OrgID, m.GroupID)
	return err
}

func (s *Store) RemoveGroupMember(ctx context.Context, orgID, groupID, memberType, memberID string) error {
	res, err := s.db.ExecContext(ctx,
		`DELETE FROM group_members WHERE org_id = ? AND group_id = ? AND member_type = ? AND member_id = ?`,
		orgID, groupID, memberType, memberID)
	if err != nil {
		return wrap("remove group member", err)
	}
	return affected("remove group member", res)
}

func (s *Store) ListGroupMembers(ctx context.Context, orgID, groupID string) ([]*store.GroupMember, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT org_id, group_id, member_type, member_id, created_at FROM group_members
		 WHERE org_id = ? AND group_id = ? ORDER BY member_type, member_id`, orgID, groupID)
	if err != nil {
		return nil, wrap("list group members", err)
	}
	defer rows.Close()
	var out []*store.GroupMember
	for rows.Next() {
		var m store.GroupMember
		var created int64
		if err := rows.Scan(&m.OrgID, &m.GroupID, &m.MemberType, &m.MemberID, &created); err != nil {
			return nil, wrap("scan group member", err)
		}
		m.CreatedAt = fromNS(created)
		out = append(out, &m)
	}
	return out, wrap("list group members", rows.Err())
}

func (s *Store) ListMemberGroups(ctx context.Context, orgID, memberType, memberID string) ([]*store.Group, error) {
	return s.queryGroups(ctx,
		`SELECT g.id, g.org_id, g.name, g.created_at FROM groups g
		 JOIN group_members m ON m.group_id = g.id
		 WHERE g.org_id = ? AND m.org_id = g.org_id AND m.member_type = ? AND m.member_id = ?
		 ORDER BY g.name, g.id`, orgID, memberType, memberID)
}

func (s *Store) queryGroups(ctx context.Context, query string, args ...any) ([]*store.Group, error) {
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, wrap("list groups", err)
	}
	defer rows.Close()
	var out []*store.Group
	for rows.Next() {
		g, err := scanGroup(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, wrap("list groups", rows.Err())
}

func scanGroup(scan func(...any) error) (*store.Group, error) {
	var g store.Group
	var created int64
	if err := scan(&g.ID, &g.OrgID, &g.Name, &created); err != nil {
		return nil, scanErr("scan group", err)
	}
	g.CreatedAt = fromNS(created)
	return &g, nil
}
