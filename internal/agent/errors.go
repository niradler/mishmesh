package agent

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/mishmesh/mishmesh/internal/tunnel"
)

var (
	ErrTokenRejected = errors.New("token invalid or revoked")
	ErrAgentDisabled = errors.New("agent disabled")
)

type RegistrationError struct {
	Failed  int
	Total   int
	Results []TunnelResult
}

func (e *RegistrationError) Error() string {
	return fmt.Sprintf("%d of %d tunnels failed to register", e.Failed, e.Total)
}

func isFatal(err error) bool {
	var reg *RegistrationError
	return errors.Is(err, ErrTokenRejected) || errors.Is(err, ErrAgentDisabled) || errors.As(err, &reg)
}

func fatalCause(ctx context.Context) error {
	if cause := context.Cause(ctx); cause != nil && isFatal(cause) {
		return cause
	}
	return nil
}

func classifyDialError(err error) error {
	he, ok := tunnel.AsHandshakeError(err)
	if !ok {
		return nil
	}
	switch he.StatusCode {
	case http.StatusUnauthorized:
		return fmt.Errorf("%w (gateway answered 401)", ErrTokenRejected)
	case http.StatusForbidden:
		return fmt.Errorf("%w (gateway answered 403)", ErrAgentDisabled)
	default:
		return nil
	}
}
