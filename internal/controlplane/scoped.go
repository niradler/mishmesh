package controlplane

import (
	"context"
	"net/http"
)

func mapSlice[T, D any](items []T, convert func(T) D) []D {
	out := make([]D, 0, len(items))
	for _, item := range items {
		out = append(out, convert(item))
	}
	return out
}

func writeList[T, D any](a *API, w http.ResponseWriter, items []T, err error, toDTO func(T) D) {
	if a.handleErr(w, err) {
		return
	}
	writeJSON(w, http.StatusOK, mapSlice(items, toDTO))
}

func loadScoped[T any](a *API, w http.ResponseWriter, r *http.Request, id string, get func(context.Context, string) (T, error), orgOf func(T) string) (T, bool) {
	var zero T
	record, err := get(r.Context(), id)
	if a.handleErr(w, err) {
		return zero, false
	}
	if orgOf(record) != a.orgScope(r) {
		writeError(w, http.StatusNotFound, "not found")
		return zero, false
	}
	return record, true
}
