package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"net/url"
	"os"
	"strings"
)

func HasEncryptedConnections(ctx context.Context, dsn string) (bool, error) {
	base, query, _ := strings.Cut(dsn, "?")
	values, err := url.ParseQuery(query)
	if err != nil {
		return false, err
	}
	if base == ":memory:" || base == "file::memory:" || values.Get("mode") == "memory" {
		return false, nil
	}
	path := strings.TrimPrefix(base, "file:")
	if decoded, err := url.PathUnescape(path); err == nil {
		path = decoded
	}
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		return false, nil
	} else if err != nil {
		return false, err
	}
	values.Set("mode", "ro")
	if !strings.HasPrefix(base, "file:") {
		base = "file:" + base
	}
	db, err := sql.Open("sqlite", base+"?"+values.Encode())
	if err != nil {
		return false, err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if err := validateSchema(ctx, db); err != nil {
		return false, err
	}
	for _, probe := range []struct{ table, query string }{
		{"connections", `SELECT EXISTS(SELECT 1 FROM connections WHERE length(secrets_enc)>0)`},
		{"org_secrets", `SELECT EXISTS(SELECT 1 FROM org_secrets)`},
		{"org_cas", `SELECT EXISTS(SELECT 1 FROM org_cas)`},
	} {
		var exists bool
		if err := db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM sqlite_master WHERE type='table' AND name=?)`, probe.table).Scan(&exists); err != nil {
			return false, err
		}
		if !exists {
			continue
		}
		var encrypted bool
		if err := db.QueryRowContext(ctx, probe.query).Scan(&encrypted); err != nil {
			return false, err
		}
		if encrypted {
			return true, nil
		}
	}
	return false, nil
}
