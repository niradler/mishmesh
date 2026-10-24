package store

import "time"

type OrgCA struct {
	OrgID     string
	CertDER   []byte
	KeyEnc    []byte
	CreatedAt time.Time
}

func OrgCAKeyContext(orgID string) string {
	return "org-ca:" + orgID
}

type OrgDecrypt struct {
	OrgID     string
	Allow     []string
	Bypass    []string
	UpdatedAt time.Time
}
