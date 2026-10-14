package authz

import (
	"encoding/json"
	"errors"

	cedar "github.com/cedar-policy/cedar-go"
)

func MigrateLegacyPolicy(source string) (string, error) {
	policies, err := cedar.NewPolicySetFromBytes("legacy.cedar", []byte(source))
	if err != nil {
		return "", errors.New("invalid saved authorization policy")
	}
	raw, err := json.Marshal(policies)
	if err != nil {
		return "", err
	}
	var document any
	if err := json.Unmarshal(raw, &document); err != nil {
		return "", err
	}
	changed := false
	var entity func(any)
	entity = func(value any) {
		switch node := value.(type) {
		case map[string]any:
			if node["type"] == "Action" && node["id"] == "cloudflare:manage" {
				node["id"] = "connection:manage"
				changed = true
			}
		case []any:
			for _, item := range node {
				entity(item)
			}
		}
	}
	var visit func(any)
	visit = func(value any) {
		switch node := value.(type) {
		case map[string]any:
			for key, item := range node {
				if key == "entity" || key == "entities" || key == "__entity" {
					entity(item)
				}
				visit(item)
			}
		case []any:
			for _, item := range node {
				visit(item)
			}
		}
	}
	visit(document)
	if !changed {
		return source, nil
	}
	raw, err = json.Marshal(document)
	if err != nil {
		return "", err
	}
	var migrated cedar.PolicySet
	if err := json.Unmarshal(raw, &migrated); err != nil {
		return "", errors.New("invalid converted authorization policy")
	}
	result := string(migrated.MarshalCedar())
	if _, err := New([]byte(result)); err != nil {
		return "", errors.New("invalid converted authorization policy")
	}
	return result, nil
}
