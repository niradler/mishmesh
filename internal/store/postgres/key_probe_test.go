package postgres

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/mishmesh/mishmesh/internal/store"
)

func TestShouldProbeCurrentCiphertextWithoutChangingPostgres(t *testing.T) {
	dsn := freshDatabaseDSN(t)
	ctx := context.Background()
	encrypted, err := HasEncryptedConnections(ctx, dsn, PoolConfig{})
	if err != nil || encrypted {
		t.Fatalf("empty probe: %v %v", encrypted, err)
	}
	raw, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	var tables int
	if err := raw.QueryRow(`SELECT count(*) FROM information_schema.tables WHERE table_schema=current_schema()`).Scan(&tables); err != nil || tables != 0 {
		t.Fatalf("probe changed schema: %d %v", tables, err)
	}
	data, err := Open(dsn, PoolConfig{})
	if err != nil {
		t.Fatal(err)
	}
	defer data.Close()
	now := time.Now()
	if err := data.CreateOrg(ctx, &store.Org{ID: "probe-org", Name: "probe", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	row := &store.Connection{ID: "probe-conn", OrgID: "probe-org", Name: "probe", Type: "example", Config: []byte(`{}`), SecretsEnc: []byte{1, 2}, CreatedAt: now, UpdatedAt: now}
	if err := data.CreateConnection(ctx, row); err != nil {
		t.Fatal(err)
	}
	encrypted, err = HasEncryptedConnections(ctx, dsn, PoolConfig{})
	if err != nil || !encrypted {
		t.Fatalf("current ciphertext probe: %v %v", encrypted, err)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := HasEncryptedConnections(canceled, dsn, PoolConfig{}); err == nil {
		t.Fatal("canceled probe succeeded")
	}
}
