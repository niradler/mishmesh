package postgres

import (
	"bytes"
	"context"
	"database/sql"
	"sync"
	"testing"

	"github.com/mishmesh/mishmesh/internal/secrets"
	"github.com/mishmesh/mishmesh/internal/store/migrationtest"
)

func legacyMigrationFixture(t *testing.T) (string, *secrets.Box) {
	t.Helper()
	t.Setenv("MISHMESH_TEST_RETAIN_FIXTURES", "true")
	dsn := freshDatabaseDSN(t)
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	box, err := secrets.New(bytes.Repeat([]byte{7}, 32))
	if err != nil {
		t.Fatal(err)
	}
	if err := migrationtest.Populate(context.Background(), db, true, box); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	return dsn, box
}

func TestShouldAtomicallyMigrateLiteralBeta4WithConcurrentPostgresOpen(t *testing.T) {
	dsn, box := legacyMigrationFixture(t)
	const workers = 8
	start := make(chan struct{})
	results := make(chan error, workers)
	identities := make(chan string, workers)
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			data, err := OpenWithBox(dsn, PoolConfig{MaxOpenConns: 2}, box)
			if err == nil {
				var id string
				id, err = migrationtest.AssertConverted(context.Background(), data, box)
				identities <- id
				data.Close()
			}
			results <- err
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	close(identities)
	for err := range results {
		if err != nil {
			t.Fatal(err)
		}
	}
	want := ""
	for id := range identities {
		if want == "" {
			want = id
		}
		if id != want {
			t.Fatal("concurrent migration allocated different identities")
		}
	}
	data, err := OpenWithBox(dsn, PoolConfig{}, box)
	if err != nil {
		t.Fatal(err)
	}
	defer data.Close()
	id, err := migrationtest.AssertConverted(context.Background(), data, box)
	if err != nil || id != want {
		t.Fatalf("reopen identity %s %s %v", id, want, err)
	}
	var count int
	if err := data.db.QueryRow(`SELECT COUNT(*) FROM information_schema.tables WHERE table_schema=current_schema() AND table_name='cloudflare_connections'`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("legacy table %d %v", count, err)
	}
	if err := data.db.QueryRow(`SELECT COUNT(*) FROM information_schema.columns WHERE table_schema=current_schema() AND table_name='services' AND column_name='spec'`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("legacy spec %d %v", count, err)
	}
	if err := data.db.QueryRow(`SELECT COUNT(*) FROM connections WHERE config LIKE '%legacy-tunnel-secret-sentinel%' OR config LIKE '%legacy-api-secret-sentinel%'`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("plaintext persisted %d %v", count, err)
	}
	wrong, _ := secrets.New(bytes.Repeat([]byte{9}, 32))
	invalid, err := OpenWithBox(dsn, PoolConfig{}, wrong)
	if invalid != nil {
		invalid.Close()
	}
	if err == nil {
		t.Fatal("wrong current key accepted")
	}
}

func TestShouldRollbackEntirePostgresLegacyMigration(t *testing.T) {
	for _, failure := range []string{"missing-key", "wrong-key", "malformed-service", "malformed-policy", "malformed-endpoint", "orphan-access"} {
		t.Run(failure, func(t *testing.T) {
			dsn, box := legacyMigrationFixture(t)
			db, err := sql.Open("pgx", dsn)
			if err != nil {
				t.Fatal(err)
			}
			switch failure {
			case "malformed-service":
				_, err = db.Exec(`UPDATE services SET spec='{invalid' WHERE id='legacy-exec-3'`)
			case "malformed-policy":
				_, err = db.Exec(`UPDATE org_policies SET cedar_src='permit (invalid)'`)
			case "malformed-endpoint":
				_, err = db.Exec(`UPDATE endpoints SET policy='{"public_access":"cloudflare","public_access":"other"}' WHERE id='legacy-endpoint-11'`)
			case "orphan-access":
				_, err = db.Exec(`INSERT INTO orgs(id,name,created_at) VALUES('orphan','orphan',0); UPDATE endpoints SET org_id='orphan' WHERE id='legacy-endpoint-11'`)
			}
			if err != nil {
				t.Fatal(err)
			}
			db.Close()
			input := box
			if failure == "missing-key" {
				input = nil
			}
			if failure == "wrong-key" {
				input, _ = secrets.New(bytes.Repeat([]byte{8}, 32))
			}
			data, err := OpenWithBox(dsn, PoolConfig{}, input)
			if data != nil {
				data.Close()
			}
			if err == nil {
				t.Fatal("failed migration accepted")
			}
			db, err = sql.Open("pgx", dsn)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			var count int
			if err := db.QueryRow(`SELECT COUNT(*) FROM cloudflare_connections`).Scan(&count); err != nil || count != 1 {
				t.Fatalf("legacy connection changed %d %v", count, err)
			}
			if err := db.QueryRow(`SELECT COUNT(*) FROM services WHERE driver='exec'`).Scan(&count); err != nil || count != 3 {
				t.Fatalf("services partially converted %d %v", count, err)
			}
			if err := db.QueryRow(`SELECT COUNT(*) FROM information_schema.tables WHERE table_schema=current_schema() AND table_name='connections'`).Scan(&count); err != nil || count != 0 {
				t.Fatalf("new schema partially committed %d %v", count, err)
			}
			if err := db.QueryRow(`SELECT COUNT(*) FROM endpoints WHERE policy LIKE '%public_access%'`).Scan(&count); err != nil || count != 13 {
				t.Fatalf("endpoint migration partially committed %d %v", count, err)
			}
		})
	}
}
