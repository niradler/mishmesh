package sqlite

import (
	"context"
	"strings"
	"time"

	"github.com/mishmesh/mishmesh/internal/store"
)

const accessLogSchema = `
CREATE TABLE IF NOT EXISTS access_log (
  id             TEXT PRIMARY KEY,
  org_id         TEXT NOT NULL,
  at             INTEGER NOT NULL,
  source         TEXT NOT NULL,
  decision       TEXT NOT NULL,
  reason         TEXT NOT NULL DEFAULT '',
  principal_type TEXT NOT NULL DEFAULT '',
  principal_id   TEXT NOT NULL DEFAULT '',
  session_id     TEXT NOT NULL DEFAULT '',
  agent_id       TEXT NOT NULL DEFAULT '',
  exit_agent_id  TEXT NOT NULL DEFAULT '',
  endpoint_id    TEXT NOT NULL DEFAULT '',
  destination    TEXT NOT NULL DEFAULT '',
  policy_ids     TEXT NOT NULL DEFAULT '',
  client_ip      TEXT NOT NULL DEFAULT '',
  method         TEXT NOT NULL DEFAULT '',
  path           TEXT NOT NULL DEFAULT '',
  status         INTEGER NOT NULL DEFAULT 0,
  protocol       TEXT NOT NULL DEFAULT '',
  bytes_in       INTEGER NOT NULL DEFAULT 0,
  bytes_out      INTEGER NOT NULL DEFAULT 0,
  duration_ms    INTEGER NOT NULL DEFAULT 0,
  latency_ms     INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS access_log_org_at ON access_log (org_id, at DESC);
CREATE INDEX IF NOT EXISTS access_log_at ON access_log (at);
`

const accessEventCols = `id, org_id, at, source, decision, reason, principal_type, principal_id, session_id, agent_id, exit_agent_id, endpoint_id, destination, policy_ids, client_ip, method, path, status, protocol, bytes_in, bytes_out, duration_ms, latency_ms`

func (s *Store) AppendAccessEvents(ctx context.Context, events []*store.AccessEvent) error {
	if len(events) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return wrap("append access events", err)
	}
	defer tx.Rollback()
	stmt, err := tx.PrepareContext(ctx, `INSERT INTO access_log (`+accessEventCols+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return wrap("append access events", err)
	}
	defer stmt.Close()
	for _, e := range events {
		if _, err := stmt.ExecContext(ctx, e.ID, e.OrgID, ns(e.At), e.Source, e.Decision, e.Reason, e.PrincipalType, e.PrincipalID, e.SessionID,
			e.AgentID, e.ExitAgentID, e.EndpointID, e.Destination, strings.Join(e.PolicyIDs, ","), e.ClientIP, e.Method, e.Path, e.Status, e.Protocol,
			e.BytesIn, e.BytesOut, e.DurationMs, e.LatencyMs); err != nil {
			return wrap("append access events", err)
		}
	}
	return wrap("append access events", tx.Commit())
}

func (s *Store) ListAccessEvents(ctx context.Context, orgID string, q store.AccessLogQuery) ([]*store.AccessEvent, error) {
	where := []string{"org_id = ?"}
	args := []any{orgID}
	for col, val := range map[string]string{"decision": q.Decision, "source": q.Source, "principal_id": q.PrincipalID, "endpoint_id": q.EndpointID} {
		if val != "" {
			where = append(where, col+" = ?")
			args = append(args, val)
		}
	}
	if !q.Since.IsZero() {
		where = append(where, "at >= ?")
		args = append(args, ns(q.Since))
	}
	if !q.Until.IsZero() {
		where = append(where, "at < ?")
		args = append(args, ns(q.Until))
	}
	args = append(args, q.EffectiveLimit())
	rows, err := s.db.QueryContext(ctx, `SELECT `+accessEventCols+` FROM access_log WHERE `+strings.Join(where, " AND ")+` ORDER BY at DESC, id DESC LIMIT ?`, args...)
	if err != nil {
		return nil, wrap("list access events", err)
	}
	defer rows.Close()
	var out []*store.AccessEvent
	for rows.Next() {
		var e store.AccessEvent
		var at int64
		var policies string
		if err := rows.Scan(&e.ID, &e.OrgID, &at, &e.Source, &e.Decision, &e.Reason, &e.PrincipalType, &e.PrincipalID, &e.SessionID,
			&e.AgentID, &e.ExitAgentID, &e.EndpointID, &e.Destination, &policies, &e.ClientIP, &e.Method, &e.Path, &e.Status, &e.Protocol,
			&e.BytesIn, &e.BytesOut, &e.DurationMs, &e.LatencyMs); err != nil {
			return nil, scanErr("scan access event", err)
		}
		e.At = fromNS(at)
		if policies != "" {
			e.PolicyIDs = strings.Split(policies, ",")
		}
		out = append(out, &e)
	}
	return out, wrap("list access events", rows.Err())
}

func (s *Store) PruneAccessEvents(ctx context.Context, before time.Time) (int64, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM access_log WHERE at < ?`, ns(before))
	if err != nil {
		return 0, wrap("prune access events", err)
	}
	n, err := res.RowsAffected()
	return n, wrap("prune access events", err)
}
