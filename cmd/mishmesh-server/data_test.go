package main

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mishmesh/mishmesh/internal/config"
	"github.com/mishmesh/mishmesh/internal/store/sqlite"
)

func TestShouldRefuseMissingKeyBeforeMutatingDatabase(t *testing.T) {
	for _, schema := range []string{
		`CREATE TABLE cloudflare_connections(tunnel_token_enc BLOB,api_token_enc BLOB); INSERT INTO cloudflare_connections VALUES(x'0102',NULL)`,
		`CREATE TABLE connections(secrets_enc BLOB); INSERT INTO connections VALUES(x'0102')`,
	} {
		t.Run(schema[:30], func(t *testing.T) {
			directory := t.TempDir()
			dsn := filepath.Join(directory, "fixture.db")
			db, err := sql.Open("sqlite", dsn)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(schema); err != nil {
				t.Fatal(err)
			}
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(dsn)
			if err != nil {
				t.Fatal(err)
			}
			cfg := config.Server{DataDSN: dsn, SecretKeyFile: filepath.Join(directory, "missing.key")}
			if _, _, err := openDataStoreWithBox(cfg); err == nil || !strings.Contains(err.Error(), "matching key") {
				t.Fatalf("missing key error: %v", err)
			}
			after, err := os.ReadFile(dsn)
			if err != nil || string(before) != string(after) {
				t.Fatalf("database changed before missing-key refusal: %v", err)
			}
			if _, err := os.Stat(cfg.SecretKeyFile); !os.IsNotExist(err) {
				t.Fatalf("replacement key created: %v", err)
			}
		})
	}
}

func TestShouldCreateKeyBeforeOpeningFreshDatabaseAndReuseIt(t *testing.T) {
	directory := t.TempDir()
	cfg := config.Server{DataDSN: filepath.Join(directory, "fresh.db"), SecretKeyFile: filepath.Join(directory, "fixture.key")}
	data, first, err := openDataStoreWithBox(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := data.Close(); err != nil {
		t.Fatal(err)
	}
	sealed, err := first.Seal([]byte("synthetic fixture"), "test")
	if err != nil {
		t.Fatal(err)
	}
	data, reopened, err := openDataStoreWithBox(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer data.Close()
	plain, err := reopened.Open(sealed, "test")
	if err != nil || string(plain) != "synthetic fixture" {
		t.Fatalf("key changed between opens: %v", err)
	}
}

func TestShouldProbeAbsentSQLiteWithoutCreatingIt(t *testing.T) {
	dsn := filepath.Join(t.TempDir(), "absent.db")
	encrypted, err := sqlite.HasEncryptedConnections(context.Background(), dsn)
	if err != nil || encrypted {
		t.Fatalf("absent probe: %v %v", encrypted, err)
	}
	if _, err := os.Stat(dsn); !os.IsNotExist(err) {
		t.Fatalf("probe created database: %v", err)
	}
}
