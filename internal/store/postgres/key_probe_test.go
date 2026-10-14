package postgres

import (
	"context"
	"database/sql"
	"testing"
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
