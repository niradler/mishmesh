package store

import "time"

type OrgSecret struct {
	OrgID     string
	Name      string
	ValueEnc  []byte
	Version   int
	CreatedAt time.Time
	UpdatedAt time.Time
}

func OrgSecretContext(orgID, name string) string {
	return "org-secret:" + orgID + ":" + name
}
