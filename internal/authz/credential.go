package authz

import (
	"errors"
	"fmt"
	"slices"
)

const CredentialAnnotation = "credential"

var ErrCredentialConflict = errors.New("determining policies select different credentials")

func SelectedCredential(d Decision) (string, error) {
	var names []string
	for _, p := range d.Policies {
		if name := p.Annotations[CredentialAnnotation]; name != "" && !slices.Contains(names, name) {
			names = append(names, name)
		}
	}
	switch len(names) {
	case 0:
		return "", nil
	case 1:
		return names[0], nil
	}
	slices.Sort(names)
	return "", fmt.Errorf("%w: %v", ErrCredentialConflict, names)
}
