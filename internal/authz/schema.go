package authz

import (
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"

	cedar "github.com/cedar-policy/cedar-go"
	xast "github.com/cedar-policy/cedar-go/x/exp/ast"
	"github.com/cedar-policy/cedar-go/x/exp/schema"
	"github.com/cedar-policy/cedar-go/x/exp/schema/resolved"
	"github.com/cedar-policy/cedar-go/x/exp/schema/validate"
)

//go:embed schema.cedarschema
var schemaSource []byte

//go:embed dataplane.cedar
var dataPlanePolicy []byte

const MaxPolicyBytes = 64 << 10

var ErrInvalidPolicy = errors.New("invalid policy")

var loadSchema = sync.OnceValues(func() (*resolved.Schema, error) {
	var s schema.Schema
	if err := s.UnmarshalCedar(schemaSource); err != nil {
		return nil, fmt.Errorf("parse embedded schema: %w", err)
	}
	return s.Resolve()
})

func Validate(src []byte) error {
	if len(src) > MaxPolicyBytes {
		return fmt.Errorf("%w: policy too large (%d bytes, max %d)", ErrInvalidPolicy, len(src), MaxPolicyBytes)
	}
	ps, err := cedar.NewPolicySetFromBytes("policy.cedar", src)
	if err != nil {
		return fmt.Errorf("%w: parse: %w", ErrInvalidPolicy, err)
	}
	resolvedSchema, err := loadSchema()
	if err != nil {
		return fmt.Errorf("load schema: %w", err)
	}
	validator := validate.New(resolvedSchema, validate.WithStrict())
	var errs []error
	for id, p := range ps.All() {
		name := policyName(id, p)
		if err := validator.Policy(name, (*xast.Policy)(p.AST())); err != nil {
			errs = append(errs, fmt.Errorf("policy %s (line %d): %w", name, p.Position().Line, err))
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("%w: %w", ErrInvalidPolicy, errors.Join(errs...))
	}
	return nil
}

func CompileOrgPolicy(matrix map[string][]Action) []byte {
	return appendDataPlane([]byte(CompileMatrix(matrix)))
}

func ForOrgSource(src []byte) []byte {
	ps, err := cedar.NewPolicySetFromBytes("policy.cedar", src)
	if err != nil {
		return src
	}
	for _, p := range ps.All() {
		scope, err := actionScopeOf(p)
		if err != nil {
			return src
		}
		for _, act := range DataPlaneActions() {
			if scope.covers(act) {
				return src
			}
		}
	}
	return appendDataPlane(src)
}

func appendDataPlane(src []byte) []byte {
	out := make([]byte, 0, len(src)+len(dataPlanePolicy)+1)
	out = append(out, src...)
	if len(src) > 0 && !strings.HasSuffix(string(src), "\n") {
		out = append(out, '\n')
	}
	return append(out, dataPlanePolicy...)
}

func policyName(id cedar.PolicyID, p *cedar.Policy) string {
	if named, ok := p.Annotations()["id"]; ok && named != "" {
		return string(named)
	}
	return string(id)
}

type policyJSON struct {
	Action struct {
		Op       string       `json:"op"`
		Entity   *entityJSON  `json:"entity"`
		Entities []entityJSON `json:"entities"`
	} `json:"action"`
	Conditions []json.RawMessage `json:"conditions"`
}

type entityJSON struct {
	Type string `json:"type"`
	ID   string `json:"id"`
}

type actionScope struct {
	all     bool
	actions map[Action]bool
}

func (s actionScope) covers(act Action) bool {
	return s.all || s.actions[act]
}

func parsePolicyJSON(p *cedar.Policy) (policyJSON, error) {
	var out policyJSON
	raw, err := p.MarshalJSON()
	if err != nil {
		return out, err
	}
	err = json.Unmarshal(raw, &out)
	return out, err
}

func actionScopeOf(p *cedar.Policy) (actionScope, error) {
	pj, err := parsePolicyJSON(p)
	if err != nil {
		return actionScope{}, err
	}
	return scopeFromJSON(pj), nil
}

func scopeFromJSON(pj policyJSON) actionScope {
	scope := actionScope{actions: map[Action]bool{}}
	switch pj.Action.Op {
	case "All":
		scope.all = true
	default:
		if pj.Action.Entity != nil && pj.Action.Entity.Type == "Action" {
			scope.actions[Action(pj.Action.Entity.ID)] = true
		}
		for _, e := range pj.Action.Entities {
			if e.Type == "Action" {
				scope.actions[Action(e.ID)] = true
			}
		}
	}
	return scope
}
