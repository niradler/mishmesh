package ingress

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"

	"github.com/mishmesh/mishmesh/internal/authz"
	"github.com/mishmesh/mishmesh/internal/store"
)

const (
	reasonCredentialUnavailable authz.Reason = "credential_unavailable"
)

var (
	errCredentialUnknown = errors.New("selected credential is not defined on the endpoint")
	errNoSecretStore     = errors.New("secret storage is not configured")
)

type SecretOpener interface {
	Open(ctx context.Context, orgID, name string) ([]byte, error)
}

type upstreamCredential struct {
	name   string
	header string
	value  string
}

func (i *Ingress) resolveCredential(ctx context.Context, ep *store.Endpoint, d authz.Decision) (*upstreamCredential, error) {
	name, err := authz.SelectedCredential(d)
	if err != nil || name == "" {
		return nil, err
	}
	spec, ok := ep.Policy.Identity.Credentials[name]
	if !ok {
		return nil, fmt.Errorf("%w: %q", errCredentialUnknown, name)
	}
	if i.secrets == nil {
		return nil, errNoSecretStore
	}
	value, err := i.secrets.Open(ctx, ep.OrgID, spec.Secret)
	if err != nil {
		return nil, fmt.Errorf("open secret %q for credential %q: %w", spec.Secret, name, err)
	}
	return &upstreamCredential{name: name, header: http.CanonicalHeaderKey(spec.Header), value: spec.Prefix + string(value)}, nil
}

func writeCredentialFailure(w http.ResponseWriter) {
	http.Error(w, "upstream credential unavailable", http.StatusBadGateway)
}

func applyUpstreamCredential(out http.Header, in *http.Request, ep *store.Endpoint) {
	if ep == nil || ep.Policy == nil || ep.Policy.Identity == nil {
		return
	}
	for _, spec := range ep.Policy.Identity.Credentials {
		out.Del(spec.Header)
	}
	if id := identityFrom(in); id != nil && id.credential != nil {
		out.Set(id.credential.header, id.credential.value)
	}
}

func mergePolicies(in []authz.DeterminingPolicy) []authz.DeterminingPolicy {
	if len(in) == 0 {
		return nil
	}
	out := slices.Clone(in)
	slices.SortStableFunc(out, func(a, b authz.DeterminingPolicy) int {
		switch {
		case a.ID < b.ID:
			return -1
		case a.ID > b.ID:
			return 1
		}
		return 0
	})
	return slices.CompactFunc(out, func(a, b authz.DeterminingPolicy) bool { return a.ID == b.ID })
}
