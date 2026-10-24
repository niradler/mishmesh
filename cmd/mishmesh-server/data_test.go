package main

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mishmesh/mishmesh/internal/config"
	"github.com/mishmesh/mishmesh/internal/secrets"
	"github.com/mishmesh/mishmesh/internal/store"
	"github.com/mishmesh/mishmesh/internal/store/sqlite"
)

func TestShouldRefuseMissingKeyBeforeMutatingTypedDatabase(t *testing.T) {
	directory := t.TempDir()
	dsn := filepath.Join(directory, "typed.db")
	data, err := sqlite.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if err := data.CreateOrg(context.Background(), &store.Org{ID: "org", Name: "org", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := data.CreateConnection(context.Background(), &store.Connection{ID: "conn", OrgID: "org", Name: "conn", Type: "cloudflare", Config: []byte(`{}`), SecretsEnc: []byte{1, 2}, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	data.Close()
	before, err := os.ReadFile(dsn)
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Server{DataDSN: dsn, SecretKeyFile: filepath.Join(directory, "missing.key")}
	if _, _, err := openDataStoreWithBox(cfg); err == nil || !strings.Contains(err.Error(), "matching key") {
		t.Fatalf("missing key: %v", err)
	}
	after, err := os.ReadFile(dsn)
	if err != nil || string(before) != string(after) {
		t.Fatalf("database changed: %v", err)
	}
	if _, err := os.Stat(cfg.SecretKeyFile); !os.IsNotExist(err) {
		t.Fatalf("replacement key created: %v", err)
	}
}

func TestShouldRejectUnsupportedDatabaseBeforeCreatingKey(t *testing.T) {
	directory := t.TempDir()
	dsn := filepath.Join(directory, "unsupported.db")
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE services(id TEXT,spec TEXT); INSERT INTO services VALUES('retained','original')`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	before, err := os.ReadFile(dsn)
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Server{DataDSN: dsn, SecretKeyFile: filepath.Join(directory, "absent.key")}
	if _, _, err := openDataStoreWithBox(cfg); !errors.Is(err, store.ErrUnsupportedSchema) {
		t.Fatalf("unsupported schema: %v", err)
	}
	after, err := os.ReadFile(dsn)
	if err != nil || string(before) != string(after) {
		t.Fatalf("database changed: %v", err)
	}
	if _, err := os.Stat(cfg.SecretKeyFile); !os.IsNotExist(err) {
		t.Fatalf("key created: %v", err)
	}
}

func TestShouldCreateKeyBeforeOpeningFreshDatabaseAndReuseIt(t *testing.T) {
	directory := t.TempDir()
	cfg := config.Server{DataDSN: filepath.Join(directory, "fresh.db"), SecretKeyFile: filepath.Join(directory, "fixture.key")}
	data, first, err := openDataStoreWithBox(cfg)
	if err != nil {
		t.Fatal(err)
	}
	data.Close()
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
		t.Fatalf("key changed: %v", err)
	}
}

func TestShouldRejectMalformedAuthenticatedCurrentSecretsWithoutChangingRows(t *testing.T) {
	for _, payload := range []string{`{"tunnel_token":null}`, `{"tunnel_token":17}`, `{"tunnel_token":"first","tunnel_token":"second"}`} {
		t.Run(payload, func(t *testing.T) {
			directory := t.TempDir()
			cfg := config.Server{DataDSN: filepath.Join(directory, "typed.db"), SecretKeyFile: filepath.Join(directory, "fixture.key")}
			data, box, err := openDataStoreWithBox(cfg)
			if err != nil {
				t.Fatal(err)
			}
			now := time.Now()
			if err := data.CreateOrg(context.Background(), &store.Org{ID: "org", Name: "org", CreatedAt: now}); err != nil {
				t.Fatal(err)
			}
			sealed, err := box.Seal([]byte(payload), "connection:org:conn")
			if err != nil {
				t.Fatal(err)
			}
			row := &store.Connection{ID: "conn", OrgID: "org", Name: "conn", Type: "cloudflare", Config: []byte(`{}`), SecretsEnc: sealed, CreatedAt: now, UpdatedAt: now}
			if err := data.CreateConnection(context.Background(), row); err != nil {
				t.Fatal(err)
			}
			data.Close()
			invalid, _, err := openDataStoreWithBox(cfg)
			if invalid != nil {
				invalid.Close()
			}
			if err == nil {
				t.Fatal("malformed secrets accepted")
			}
			reopened, err := sqlite.Open(cfg.DataDSN)
			if err != nil {
				t.Fatal(err)
			}
			defer reopened.Close()
			retained, err := reopened.GetConnection(context.Background(), row.ID)
			if err != nil || string(retained.SecretsEnc) != string(sealed) {
				t.Fatalf("ciphertext changed: %v", err)
			}
		})
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

func TestShouldRejectWrongKeyForCurrentTypedConnection(t *testing.T) {
	directory := t.TempDir()
	cfg := config.Server{DataDSN: filepath.Join(directory, "typed.db"), SecretKeyFile: filepath.Join(directory, "matching.key")}
	data, box, err := openDataStoreWithBox(cfg)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if err := data.CreateOrg(context.Background(), &store.Org{ID: "org", Name: "org", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	sealed, err := box.Seal([]byte(`{"tunnel_token":"current-synthetic-secret"}`), "connection:org:conn")
	if err != nil {
		t.Fatal(err)
	}
	if err := data.CreateConnection(context.Background(), &store.Connection{ID: "conn", OrgID: "org", Name: "conn", Type: "cloudflare", Config: []byte(`{}`), SecretsEnc: sealed, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	data.Close()
	cfg.SecretKeyFile = filepath.Join(directory, "wrong.key")
	if _, err := secrets.LoadOrCreateKey(cfg.SecretKeyFile); err != nil {
		t.Fatal(err)
	}
	invalid, _, err := openDataStoreWithBox(cfg)
	if invalid != nil {
		invalid.Close()
	}
	if err == nil || strings.Contains(err.Error(), "current-synthetic-secret") {
		t.Fatalf("wrong-key refusal: %v", err)
	}
	reopened, err := sqlite.Open(cfg.DataDSN)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	retained, err := reopened.GetConnection(context.Background(), "conn")
	if err != nil || string(retained.SecretsEnc) != string(sealed) {
		t.Fatalf("ciphertext changed: %v", err)
	}
}

func TestShouldRejectIncompleteKnownSchemasBeforeCreatingKey(t *testing.T) {
	for _, schema := range []string{
		`CREATE TABLE orgs(id TEXT PRIMARY KEY,name TEXT)`,
		`CREATE TABLE service_requires(service_id TEXT,requires_id TEXT)`,
		`CREATE TABLE endpoint_aliases(hostname TEXT,endpoint_id TEXT)`,
	} {
		t.Run(schema, func(t *testing.T) {
			directory := t.TempDir()
			dsn := filepath.Join(directory, "incomplete.db")
			db, err := sql.Open("sqlite", dsn)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(schema); err != nil {
				t.Fatal(err)
			}
			db.Close()
			before, err := os.ReadFile(dsn)
			if err != nil {
				t.Fatal(err)
			}
			cfg := config.Server{DataDSN: dsn, SecretKeyFile: filepath.Join(directory, "absent.key")}
			data, _, err := openDataStoreWithBox(cfg)
			if data != nil {
				data.Close()
			}
			if !errors.Is(err, store.ErrUnsupportedSchema) {
				t.Fatalf("incomplete schema accepted or failed late: %v", err)
			}
			after, err := os.ReadFile(dsn)
			if err != nil || string(before) != string(after) {
				t.Fatalf("database changed: %v", err)
			}
			if _, err := os.Stat(cfg.SecretKeyFile); !os.IsNotExist(err) {
				t.Fatalf("key created: %v", err)
			}
		})
	}
}

func TestShouldRefuseNewKeyWhenOrgSecretsExist(t *testing.T) {
	directory := t.TempDir()
	cfg := config.Server{DataDSN: filepath.Join(directory, "secrets.db"), SecretKeyFile: filepath.Join(directory, "original.key")}
	data, box, err := openDataStoreWithBox(cfg)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := data.CreateOrg(ctx, &store.Org{ID: "org", Name: "org", CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	sealed, err := box.Seal([]byte("synthetic"), store.OrgSecretContext("org", "api"))
	if err != nil {
		t.Fatal(err)
	}
	if err := data.PutOrgSecret(ctx, &store.OrgSecret{OrgID: "org", Name: "api", ValueEnc: sealed, UpdatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	data.Close()
	cfg.SecretKeyFile = filepath.Join(directory, "missing.key")
	reopened, _, err := openDataStoreWithBox(cfg)
	if reopened != nil {
		reopened.Close()
	}
	if err == nil {
		t.Fatal("opened org secrets with a freshly generated key")
	}
	if _, statErr := os.Stat(cfg.SecretKeyFile); !os.IsNotExist(statErr) {
		t.Fatalf("key file created: %v", statErr)
	}
}
