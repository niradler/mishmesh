package connections

import (
	"context"
	"errors"
	"slices"
	"strings"

	"github.com/mishmesh/mishmesh/internal/store"
)

func NormalizeAccessHostname(hostname string) (string, error) {
	hostname = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(hostname)), ".")
	if hostname == "" || len(hostname) > 253 {
		return "", errors.New("access hostname must be a valid DNS name")
	}
	for _, label := range strings.Split(hostname, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return "", errors.New("access hostname must be a valid DNS name")
		}
		for _, character := range label {
			if character != '-' && (character < 'a' || character > 'z') && (character < '0' || character > '9') {
				return "", errors.New("access hostname must be a valid DNS name")
			}
		}
	}
	return hostname, nil
}

func SupportsProtocol(descriptor Descriptor, protocol string) bool {
	if protocol == "" {
		protocol = store.KindHTTP
	}
	return descriptor.Role == "access" && slices.Contains(descriptor.Protocols, protocol)
}

func (r *Registry) AccessHostname(connectionType, endpointDomain, requested, protocol string) (string, error) {
	entry, found := r.lookup(connectionType)
	if !found || entry.descriptor.Role != "access" {
		return "", errors.New("connection must provide endpoint access")
	}
	if !SupportsProtocol(entry.descriptor, protocol) {
		return "", errors.New("connection does not support this endpoint protocol")
	}
	naming, supported := entry.provider.(AccessHostnameProvider)
	if !supported {
		return "", errors.New("connection does not support endpoint hostname naming")
	}
	return naming.AccessHostname(endpointDomain, requested)
}

func RecordAccessAliases(ctx context.Context, record store.Connection, descriptor Descriptor, status *Status, deps Deps) error {
	if descriptor.Role != "access" {
		return nil
	}
	aliases := []store.EndpointAlias{}
	for _, host := range status.Hostnames {
		if host.State != "published" || host.EndpointID == "" {
			continue
		}
		if host.EndpointKind == "" || !SupportsProtocol(descriptor, host.EndpointKind) {
			for index := range status.Hostnames {
				if status.Hostnames[index].State == "published" {
					status.Hostnames[index].State = "error"
				}
			}
			return errors.New("observed access protocol is unsupported")
		}
		aliases = append(aliases, store.EndpointAlias{Hostname: host.Host, EndpointID: host.EndpointID, AccessHostname: host.AccessHostname, AccessHostnameKnown: host.AccessHostnameKnown, Kind: host.EndpointKind})
	}
	publish := deps.PublishAliases
	if publish == nil {
		if deps.Data == nil {
			publish = func(context.Context, []store.EndpointAlias) error {
				return errors.New("access alias store is unavailable")
			}
		} else {
			publish = func(ctx context.Context, aliases []store.EndpointAlias) error {
				return deps.Data.ReplaceConnectionAliasesIfCurrent(ctx, &record, aliases)
			}
		}
	}
	if err := publish(ctx, aliases); err != nil {
		for index := range status.Hostnames {
			if status.Hostnames[index].State == "published" {
				status.Hostnames[index].State = "error"
				status.Hostnames[index].Detail = "Access routing changed or could not be saved; retry sync"
			}
		}
		return errors.New("access aliases could not be saved")
	}
	if deps.OnAliasesChanged != nil {
		deps.OnAliasesChanged()
	}
	return nil
}
