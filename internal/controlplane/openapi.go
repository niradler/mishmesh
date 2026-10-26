package controlplane

import (
	"encoding/json"
	"net/http"
	"regexp"
	"strings"
)

const openAPIPath = "/api/v1/openapi.json"

type routeRegistrar interface {
	HandleFunc(pattern string, handler func(http.ResponseWriter, *http.Request))
}

type apiOperation struct {
	method  string
	path    string
	secured bool
}

type routeRecorder struct {
	mux        *http.ServeMux
	operations []apiOperation
}

func (r *routeRecorder) HandleFunc(pattern string, handler func(http.ResponseWriter, *http.Request)) {
	r.record(pattern, false)
	r.mux.HandleFunc(pattern, handler)
}

func (r *routeRecorder) handleSecured(pattern string, handler http.HandlerFunc) {
	r.record(pattern, true)
	r.mux.HandleFunc(pattern, handler)
}

func (r *routeRecorder) record(pattern string, secured bool) {
	method, path, ok := strings.Cut(pattern, " ")
	if !ok {
		return
	}
	r.operations = append(r.operations, apiOperation{method: method, path: path, secured: secured})
}

func (a *API) openAPIHandler(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = w.Write(a.openAPISpec)
}

var (
	pathWildcard      = regexp.MustCompile(`\{([^}]*)\}`)
	operationIDUnsafe = regexp.MustCompile(`[^a-z0-9]+`)
)

func openAPIPathAndParams(pattern string) (string, []map[string]any) {
	pattern = strings.TrimSuffix(pattern, "{$}")
	var params []map[string]any
	path := pathWildcard.ReplaceAllStringFunc(pattern, func(segment string) string {
		name := strings.TrimSuffix(strings.Trim(segment, "{}"), "...")
		params = append(params, map[string]any{
			"name":     name,
			"in":       "path",
			"required": true,
			"schema":   map[string]string{"type": "string"},
		})
		return "{" + name + "}"
	})
	return path, params
}

func openAPIOperationID(method, path string) string {
	parts := []string{strings.ToLower(method)}
	for _, segment := range strings.Split(strings.TrimPrefix(path, "/api/v1"), "/") {
		if segment == "" {
			continue
		}
		if strings.HasPrefix(segment, "{") {
			segment = "by_" + strings.Trim(segment, "{}")
		}
		if segment = strings.Trim(operationIDUnsafe.ReplaceAllString(strings.ToLower(segment), "_"), "_"); segment != "" {
			parts = append(parts, segment)
		}
	}
	return strings.Join(parts, "_")
}

func openAPITag(path string) string {
	for _, segment := range strings.Split(strings.TrimPrefix(path, "/api/v1"), "/") {
		if segment != "" && !strings.HasPrefix(segment, "{") {
			return strings.TrimPrefix(segment, ".")
		}
	}
	return "root"
}

func buildOpenAPI(operations []apiOperation) ([]byte, error) {
	jsonContent := map[string]any{"application/json": map[string]any{"schema": map[string]any{}}}
	paths := make(map[string]map[string]any)
	for _, op := range operations {
		path, params := openAPIPathAndParams(op.path)
		operation := map[string]any{
			"operationId": openAPIOperationID(op.method, path),
			"tags":        []string{openAPITag(path)},
			"responses": map[string]any{
				"default": map[string]any{"description": "JSON response", "content": jsonContent},
			},
			"security": []map[string][]string{},
		}
		if op.secured {
			operation["security"] = []map[string][]string{{"bearer": {}}}
		}
		if len(params) > 0 {
			operation["parameters"] = params
		}
		switch op.method {
		case http.MethodPost, http.MethodPut, http.MethodPatch:
			operation["requestBody"] = map[string]any{
				"required": false,
				"content":  map[string]any{"application/json": map[string]any{"schema": map[string]string{"type": "object"}}},
			}
		}
		if paths[path] == nil {
			paths[path] = make(map[string]any)
		}
		paths[path][strings.ToLower(op.method)] = operation
	}
	doc := map[string]any{
		"openapi": "3.1.0",
		"info":    map[string]string{"title": "mishmesh control API", "version": "v1"},
		"paths":   paths,
		"components": map[string]any{
			"securitySchemes": map[string]any{
				"bearer": map[string]string{"type": "http", "scheme": "bearer"},
			},
		},
	}
	spec, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(spec, '\n'), nil
}
