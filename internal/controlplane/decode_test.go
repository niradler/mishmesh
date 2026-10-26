package controlplane

import (
	"net/http"
	"strings"
	"testing"
)

func TestDecodeJSONRejectsTrailingData(t *testing.T) {
	_, srv := newTestAPI(t)
	for _, body := range []string{
		`{"name":"a"}{"name":"b"}`,
		`{"name":"a"} garbage`,
		`{"name":"a"}[]`,
	} {
		do(t, srv, http.MethodPost, "/api/v1/agents", body, http.StatusBadRequest, nil)
	}
	do(t, srv, http.MethodPost, "/api/v1/agents", `{"name":"a"}  `+"\n", http.StatusCreated, nil)
}

func TestDecodeJSONOversizeBodyIs413(t *testing.T) {
	_, srv := newTestAPI(t)
	huge := `{"name":"` + strings.Repeat("a", 2<<20) + `"}`
	do(t, srv, http.MethodPost, "/api/v1/agents", huge, http.StatusRequestEntityTooLarge, nil)
}

func TestConnectionDecoderStillRejectsDuplicateKeys(t *testing.T) {
	f := newNetworkFixture(t)
	do(t, f.srv, http.MethodPost, "/api/v1/connections", `{"type":"x","type":"y","name":"n"}`, http.StatusBadRequest, nil)
	huge := `{"type":"x","name":"` + strings.Repeat("a", 2<<20) + `"}`
	do(t, f.srv, http.MethodPost, "/api/v1/connections", huge, http.StatusRequestEntityTooLarge, nil)
}
