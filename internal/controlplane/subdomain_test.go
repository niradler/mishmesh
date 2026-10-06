package controlplane

import (
	"net/http"
	"testing"
)

func TestEndpointSubdomainValidation(t *testing.T) {
	tests := []struct {
		name      string
		subdomain string
		want      int
	}{
		{"valid", "shop", http.StatusCreated},
		{"uppercase is normalised", "Blog", http.StatusCreated},
		{"reserved app", "app", http.StatusBadRequest},
		{"reserved api", "api", http.StatusBadRequest},
		{"reserved www", "www", http.StatusBadRequest},
		{"reserved admin", "admin", http.StatusBadRequest},
		{"reserved login", "login", http.StatusBadRequest},
		{"reserved console", "console", http.StatusBadRequest},
		{"configured host label", "localhost", http.StatusBadRequest},
		{"dot", "a.b", http.StatusBadRequest},
		{"leading hyphen", "-x", http.StatusBadRequest},
		{"underscore", "a_b", http.StatusBadRequest},
		{"too long", "a234567890123456789012345678901234567890123456789012345678901234", http.StatusBadRequest},
		{"path chars", "../x", http.StatusBadRequest},
	}
	for _, tc := range tests {
		t.Run("create "+tc.name, func(t *testing.T) {
			_, srv := newTestAPI(t)
			var created struct {
				Agent agentDTO `json:"agent"`
			}
			do(t, srv, http.MethodPost, "/api/v1/agents", `{"name":"a"}`, http.StatusCreated, &created)
			body := `{"agent_id":"` + created.Agent.ID + `","kind":"http","subdomain":"` + tc.subdomain + `"}`
			do(t, srv, http.MethodPost, "/api/v1/endpoints", body, tc.want, nil)
		})
	}
}

func TestEndpointSubdomainPatchValidation(t *testing.T) {
	_, srv := newTestAPI(t)
	var created struct {
		Agent agentDTO `json:"agent"`
	}
	do(t, srv, http.MethodPost, "/api/v1/agents", `{"name":"a"}`, http.StatusCreated, &created)
	var ep endpointDTO
	do(t, srv, http.MethodPost, "/api/v1/endpoints", `{"agent_id":"`+created.Agent.ID+`","kind":"http","subdomain":"shop"}`, http.StatusCreated, &ep)

	for _, bad := range []string{"app", "WWW", "a.b", "-x", "admin"} {
		do(t, srv, http.MethodPatch, "/api/v1/endpoints/"+ep.ID, `{"subdomain":"`+bad+`"}`, http.StatusBadRequest, nil)
	}
	do(t, srv, http.MethodPatch, "/api/v1/endpoints/"+ep.ID, `{"subdomain":"store"}`, http.StatusOK, nil)
}
