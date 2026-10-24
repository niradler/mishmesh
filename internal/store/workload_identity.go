package store

import "time"

type WorkloadIdentity struct {
	ID               string
	OrgID            string
	ServiceAccountID string
	Issuer           string
	Audience         string
	Subject          string
	CreatedAt        time.Time
}
