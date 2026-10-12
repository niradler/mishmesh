package services

import "github.com/mishmesh/mishmesh/internal/store"

func ValidateRequirements(orgID, serviceID string, requires []string, services []*store.Service) error {
	return store.ValidateRequirements(orgID, serviceID, requires, services)
}
