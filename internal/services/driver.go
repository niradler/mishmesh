package services

import "context"

type State string

const (
	Running State = "running"
	Stopped State = "stopped"
	Partial State = "partial"
	Unknown State = "unknown"
)

type Driver interface {
	Start(ctx context.Context) error
	Stop(ctx context.Context) error
	State(ctx context.Context) (State, error)
}
