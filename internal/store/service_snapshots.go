package store

import (
	"bytes"
	"context"
	"slices"
)

type ServiceMutation interface {
	CreateService(context.Context, *Service) error
	UpdateService(context.Context, *Service) error
	DeleteService(context.Context, string) error
	CheckDeleteService(context.Context, string) error
	ListAllServices(context.Context) ([]*Service, error)
	ListEndpointsByOrg(context.Context, string) ([]*Endpoint, error)
}

type ServiceSnapshotStore interface {
	WithServiceSnapshots(context.Context, string, []*Service, func(ServiceMutation) error) error
	ValidateServiceSnapshots(context.Context, string, []*Service) error
	UpdateServiceIfCurrent(context.Context, *Service, []*Service) error
	DeleteServiceIfCurrent(context.Context, string, string, []*Service) error
}

func SameServiceInput(left, right Service) bool {
	return left.ID == right.ID && left.OrgID == right.OrgID && left.Name == right.Name && left.Driver == right.Driver && bytes.Equal(left.Config, right.Config) && bytes.Equal(left.Ready, right.Ready) && sameServiceIDs(left.Requires, right.Requires) && sameServiceIDs(left.ConnectionIDs, right.ConnectionIDs) && left.IdleTimeoutSeconds == right.IdleTimeoutSeconds && left.StartTimeoutSeconds == right.StartTimeoutSeconds && left.UpdatedAt.Equal(right.UpdatedAt)
}

func sameServiceIDs(left, right []string) bool {
	left = slices.Clone(left)
	right = slices.Clone(right)
	slices.Sort(left)
	slices.Sort(right)
	return slices.Equal(slices.Compact(left), slices.Compact(right))
}
