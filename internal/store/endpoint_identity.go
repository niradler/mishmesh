package store

import (
	"fmt"
	"net/http"
	"regexp"
	"strings"
)

const (
	maxEndpointCredentials = 16
	maxCredentialPrefix    = 64
)

var (
	credentialNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,62}$`)
	headerNamePattern     = regexp.MustCompile("^[!#$%&'*+.^_`|~0-9A-Za-z-]{1,128}$")
	reservedCredentialHdr = map[string]bool{
		"Host": true, "Connection": true, "Upgrade": true, "Te": true, "Trailer": true,
		"Transfer-Encoding": true, "Content-Length": true, "Keep-Alive": true, "Proxy-Connection": true,
		"Proxy-Authorization": true, "Cookie": true, "Forwarded": true, "X-Real-Ip": true,
	}
)

type EndpointIdentity struct {
	Required            bool                          `json:"required,omitempty"`
	AuthorizationHeader bool                          `json:"authorization_header,omitempty"`
	BrowserLogin        bool                          `json:"browser_login,omitempty"`
	Credentials         map[string]EndpointCredential `json:"credentials,omitempty"`
}

type EndpointCredential struct {
	Header string `json:"header"`
	Secret string `json:"secret"`
	Prefix string `json:"prefix,omitempty"`
}

func ValidCredentialName(name string) bool {
	return credentialNamePattern.MatchString(name)
}

func (e *EndpointIdentity) Validate() error {
	if e == nil {
		return nil
	}
	if len(e.Credentials) > maxEndpointCredentials {
		return fmt.Errorf("identity.credentials allows at most %d entries", maxEndpointCredentials)
	}
	for name, c := range e.Credentials {
		if !ValidCredentialName(name) {
			return fmt.Errorf("identity.credentials name %q must match %s", name, credentialNamePattern)
		}
		if err := c.validate("identity.credentials." + name); err != nil {
			return err
		}
	}
	return nil
}

func (c EndpointCredential) validate(field string) error {
	if !ValidCredentialName(c.Secret) {
		return fmt.Errorf("%s.secret must be a secret name", field)
	}
	if !headerNamePattern.MatchString(c.Header) {
		return fmt.Errorf("%s.header is not a valid header name", field)
	}
	canonical := http.CanonicalHeaderKey(c.Header)
	if reservedCredentialHdr[canonical] || strings.HasPrefix(canonical, "X-Mishmesh-") || strings.HasPrefix(canonical, "X-Forwarded-") {
		return fmt.Errorf("%s.header %q is reserved", field, c.Header)
	}
	if len(c.Prefix) > maxCredentialPrefix || strings.ContainsAny(c.Prefix, "\r\n\x00") {
		return fmt.Errorf("%s.prefix is invalid", field)
	}
	return nil
}
