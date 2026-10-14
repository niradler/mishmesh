package sqlite

import (
	"bytes"
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"github.com/mishmesh/mishmesh/internal/secrets"
	"github.com/mishmesh/mishmesh/internal/store/migrationtest"
)

func legacyMigrationFixture(t *testing.T) (string, *secrets.Box) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "literal-beta4.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	box, err := secrets.New(bytes.Repeat([]byte{7}, 32))
	if err != nil {
		t.Fatal(err)
	}
	if err := migrationtest.Populate(context.Background(), db, false, box); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	return path, box
}

func TestShouldAtomicallyMigrateLiteralBeta4AndReopenWithoutChangingIdentity(t *testing.T) {
	path, box := legacyMigrationFixture(t)
	data, err := OpenWithBox(path, box)
	if err != nil {
		t.Fatal(err)
	}
	id, err := migrationtest.AssertConverted(context.Background(), data, box)
	if err != nil {
		data.Close()
		t.Fatal(err)
	}
	var legacy int
	if err := data.db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='cloudflare_connections'`).Scan(&legacy); err != nil || legacy != 0 {
		t.Fatalf("legacy table remains %d %v", legacy, err)
	}
	if err := data.db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('services') WHERE name='spec'`).Scan(&legacy); err != nil || legacy != 0 {
		t.Fatalf("legacy spec remains %d %v", legacy, err)
	}
	if err := data.Close(); err != nil {
		t.Fatal(err)
	}
	data, err = OpenWithBox(path, box)
	if err != nil {
		t.Fatal(err)
	}
	defer data.Close()
	reopened, err := migrationtest.AssertConverted(context.Background(), data, box)
	if err != nil || reopened != id {
		t.Fatalf("non-idempotent migration %q %q %v", id, reopened, err)
	}
	for _, file := range []string{path, path + "-wal"} {
		raw, err := os.ReadFile(file)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		for _, secret := range []string{migrationtest.TunnelToken, migrationtest.APIToken} {
			if bytes.Contains(raw, []byte(secret)) {
				t.Fatal("connection secret persisted in plaintext")
			}
		}
	}
}

func TestShouldRollbackEntireLegacyMigrationForMissingWrongKeyOrMalformedRecord(t *testing.T) {
	for _, failure := range []string{"missing-key", "wrong-key", "malformed-service", "malformed-policy", "malformed-endpoint", "orphan-access"} {
		t.Run(failure, func(t *testing.T) {
			path, box := legacyMigrationFixture(t)
			db, err := sql.Open("sqlite", path)
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
				_, err = db.Exec(`UPDATE endpoints SET org_id='orphan' WHERE id='legacy-endpoint-11'`)
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
			data, err := OpenWithBox(path, input)
			if data != nil {
				data.Close()
			}
			if err == nil {
				t.Fatal("failed migration accepted")
			}
			db, err = sql.Open("sqlite", path)
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
			if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='connections'`).Scan(&count); err != nil || count != 0 {
				t.Fatalf("new schema partially committed %d %v", count, err)
			}
			if err := db.QueryRow(`SELECT COUNT(*) FROM endpoints WHERE policy LIKE '%public_access%'`).Scan(&count); err != nil || count != 13 {
				t.Fatalf("endpoint migration partially committed %d %v", count, err)
			}
		})
	}
}

func TestShouldCreateFreshTypedSchemaAndRejectWrongKeyOnCurrentCiphertext(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fresh.db")
	data, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	var count int
	if err := data.db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE name='cloudflare_connections'`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("fresh legacy schema created %d %v", count, err)
	}
	data.Close()
	path, box := legacyMigrationFixture(t)
	data, err = OpenWithBox(path, box)
	if err != nil {
		t.Fatal(err)
	}
	data.Close()
	wrong, _ := secrets.New(bytes.Repeat([]byte{9}, 32))
	data, err = OpenWithBox(path, wrong)
	if data != nil {
		data.Close()
	}
	if err == nil {
		t.Fatal("wrong current key accepted")
	}
}
