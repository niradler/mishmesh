package cloudflare

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
)

func pagingServer(t *testing.T, total, pageSize int, requests *[]string) *Client {
	t.Helper()
	mux := http.NewServeMux()
	serve := func(w http.ResponseWriter, r *http.Request, item func(i int) any) {
		*requests = append(*requests, r.URL.Path+"?"+r.URL.RawQuery)
		page, _ := strconv.Atoi(r.URL.Query().Get("page"))
		if page < 1 {
			page = 1
		}
		size := pageSize
		if v, err := strconv.Atoi(r.URL.Query().Get("per_page")); err == nil && v > 0 && v < size {
			size = v
		}
		start := (page - 1) * size
		var items []any
		for i := start; i < min(start+size, total); i++ {
			items = append(items, item(i))
		}
		if items == nil {
			items = []any{}
		}
		totalPages := (total + size - 1) / size
		_ = json.NewEncoder(w).Encode(map[string]any{
			"success":     true,
			"errors":      []any{},
			"result":      items,
			"result_info": map[string]any{"page": page, "total_pages": totalPages},
		})
	}
	mux.HandleFunc("GET /zones/{zone}/dns_records", func(w http.ResponseWriter, r *http.Request) {
		serve(w, r, func(i int) any {
			return DNSRecord{ID: fmt.Sprintf("rec-%d", i), Type: "CNAME", Name: r.URL.Query().Get("name")}
		})
	})
	mux.HandleFunc("GET /zones", func(w http.ResponseWriter, r *http.Request) {
		serve(w, r, func(i int) any { return Zone{ID: fmt.Sprintf("z-%d", i), Name: fmt.Sprintf("z%d.example.com", i)} })
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return NewClient(srv.URL, "token")
}

func TestListDNSRecordsFollowsEveryPage(t *testing.T) {
	var requests []string
	client := pagingServer(t, 7, 3, &requests)

	records, err := client.ListDNSRecords(context.Background(), "z1", "app.example.com")
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 7 {
		t.Fatalf("got %d records over %d requests %v, want 7", len(records), len(requests), requests)
	}
	for i, rec := range records {
		if want := fmt.Sprintf("rec-%d", i); rec.ID != want {
			t.Fatalf("record %d = %s, want %s", i, rec.ID, want)
		}
	}
}

func TestListZonesFollowsEveryPage(t *testing.T) {
	var requests []string
	client := pagingServer(t, 5, 2, &requests)

	zones, err := client.ListZones(context.Background(), "acct")
	if err != nil {
		t.Fatal(err)
	}
	if len(zones) != 5 {
		t.Fatalf("got %d zones, want 5", len(zones))
	}
}
