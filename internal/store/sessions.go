package store

import (
	"context"
	"time"
)

const (
	SubjectUser           = GroupMemberUser
	SubjectServiceAccount = GroupMemberServiceAccount
	SubjectGuest          = "guest"
)

type TokenSession struct {
	ID          string    `json:"id"`
	OrgID       string    `json:"org"`
	SubjectType string    `json:"subject_type"`
	SubjectID   string    `json:"subject_id"`
	TokenID     string    `json:"tid"`
	AuthMethod  string    `json:"auth_method"`
	AuthTime    time.Time `json:"auth_time"`
	CreatedAt   time.Time `json:"created_at"`
	ExpiresAt   time.Time `json:"expires_at"`
	EndpointID  string    `json:"endpoint_id,omitempty"`
	AgentID     string    `json:"agent_id,omitempty"`
	PrevTokenID string    `json:"prev_tid,omitempty"`
	RotatedAt   time.Time `json:"rotated_at,omitzero"`
}

type SessionStore interface {
	PutTokenSession(ctx context.Context, s *TokenSession) error
	RotateTokenSession(ctx context.Context, s *TokenSession, prevTokenID string) error
	GetTokenSession(ctx context.Context, id string) (*TokenSession, error)
	DeleteTokenSession(ctx context.Context, id string) error
	DeleteSubjectSessions(ctx context.Context, orgID, subjectType, subjectID string) ([]string, error)
	ClaimOnce(ctx context.Context, key string, ttl time.Duration) (bool, error)
	PutOnce(ctx context.Context, key string, value []byte, ttl time.Duration) error
	TakeOnce(ctx context.Context, key string) ([]byte, error)
	PeekOnce(ctx context.Context, key string) ([]byte, error)
}

func SubjectKey(orgID, subjectType, subjectID string) string {
	return orgID + "\x00" + subjectType + "\x00" + subjectID
}
