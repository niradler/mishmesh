package postgres

import (
	"context"
	"database/sql"
	"testing"

	"github.com/mishmesh/mishmesh/internal/store/migrationtest"
)

func TestShouldProbeCiphertextWithoutMigratingPostgres(t *testing.T) {
	dsn := freshDatabaseDSN(t)
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	encrypted, err := HasEncryptedConnections(ctx, dsn, PoolConfig{})
	if err != nil || encrypted {
		t.Fatalf("empty probe: %v %v", encrypted, err)
	}
	var tables int
	if err := db.QueryRow(`SELECT count(*) FROM information_schema.tables WHERE table_schema=current_schema()`).Scan(&tables); err != nil || tables != 0 {
		t.Fatalf("probe changed schema: tables=%d error=%v", tables, err)
	}
	if _, err := db.Exec(`CREATE TABLE cloudflare_connections(tunnel_token_enc BYTEA,api_token_enc BYTEA); INSERT INTO cloudflare_connections VALUES(NULL,decode('0102','hex'))`); err != nil {
		t.Fatal(err)
	}
	encrypted, err = HasEncryptedConnections(ctx, dsn, PoolConfig{})
	if err != nil || !encrypted {
		t.Fatalf("legacy API ciphertext probe: %v %v", encrypted, err)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := HasEncryptedConnections(canceled, dsn, PoolConfig{}); err == nil {
		t.Fatal("canceled probe succeeded")
	}
}

func TestShouldRejectMalformedAuthenticatedCurrentSecretsWithoutChangingRows(t *testing.T) {
	for _, payload := range []string{`{"tunnel_token":null}`, `{"tunnel_token":17}`, `{"tunnel_token":"first","tunnel_token":"second"}`} {
		t.Run(payload, func(t *testing.T) {
			dsn, box := legacyMigrationFixture(t)
			data, err := OpenWithBox(dsn, PoolConfig{}, box)
			if err != nil {
				t.Fatal(err)
			}
			rows, err := data.ListConnections(context.Background(), migrationtest.OrgID)
			if err != nil || len(rows) != 1 {
				t.Fatalf("converted rows: %d %v", len(rows), err)
			}
			row := rows[0]
			sealed, err := box.Seal([]byte(payload), "connection:"+row.OrgID+":"+row.ID)
			if err != nil {
				t.Fatal(err)
			}
			row.SecretsEnc = sealed
			if err := data.UpdateConnection(context.Background(), row); err != nil {
				t.Fatal(err)
			}
			data.Close()
			invalid, err := OpenWithBox(dsn, PoolConfig{}, box)
			if invalid != nil {
				invalid.Close()
			}
			if err == nil {
				t.Fatal("malformed authenticated secrets accepted")
			}
			db, err := sql.Open("pgx", dsn)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			var retained []byte
			if err := db.QueryRow(`SELECT secrets_enc FROM connections WHERE id=$1`, row.ID).Scan(&retained); err != nil || string(retained) != string(sealed) {
				t.Fatalf("failed open mutated ciphertext: %v", err)
			}
		})
	}
}
