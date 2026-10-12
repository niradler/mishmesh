package store

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
)

func ValidateRequirements(orgID, serviceID string, requires []string, services []*Service) error {
	if orgID == "" {
		return fmt.Errorf("requirement organization is required: %w", ErrConflict)
	}
	if serviceID == "" {
		return fmt.Errorf("requirement service is required: %w", ErrConflict)
	}
	graph := make(map[string]*Service)
	for _, service := range services {
		if service != nil && service.OrgID == orgID {
			graph[service.ID] = service
		}
	}
	prospective := Service{ID: serviceID, OrgID: orgID, Name: "service", Requires: requires}
	if current := graph[serviceID]; current != nil {
		prospective.Name = current.Name
	}
	graph[serviceID] = &prospective
	less := func(a, b string) bool {
		if graph[a].Name != graph[b].Name {
			return graph[a].Name < graph[b].Name
		}
		return a < b
	}
	edges := make(map[string][]string, len(graph))
	roots := make([]string, 0, len(graph))
	for id, service := range graph {
		roots = append(roots, id)
		seen := make(map[string]bool)
		for _, requirement := range service.Requires {
			if requirement == "" || graph[requirement] == nil {
				return fmt.Errorf("requirement must reference a service in this organization: %w", ErrConflict)
			}
			if !seen[requirement] {
				seen[requirement] = true
				edges[id] = append(edges[id], requirement)
			}
		}
		sort.Slice(edges[id], func(i, j int) bool { return less(edges[id][i], edges[id][j]) })
	}
	sort.Slice(roots, func(i, j int) bool { return less(roots[i], roots[j]) })
	state := make(map[string]uint8, len(graph))
	var path []string
	var visit func(string) error
	visit = func(id string) error {
		if state[id] == 2 {
			return nil
		}
		if state[id] == 1 {
			start := 0
			for path[start] != id {
				start++
			}
			names := make([]string, 0, len(path)-start+1)
			for _, member := range path[start:] {
				names = append(names, strconv.Quote(graph[member].Name))
			}
			names = append(names, strconv.Quote(graph[id].Name))
			return fmt.Errorf("requirement cycle: %s: %w", strings.Join(names, " -> "), ErrConflict)
		}
		state[id] = 1
		path = append(path, id)
		for _, requirement := range edges[id] {
			if err := visit(requirement); err != nil {
				return err
			}
		}
		path = path[:len(path)-1]
		state[id] = 2
		return nil
	}
	for _, id := range roots {
		if err := visit(id); err != nil {
			return err
		}
	}
	return nil
}
