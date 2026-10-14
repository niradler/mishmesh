package sqlite

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/mishmesh/mishmesh/internal/secrets"
	"github.com/mishmesh/mishmesh/internal/store"
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

func TestShouldRejectUnsupportedSchemaWithoutConvertingIt(t *testing.T) {
	path, _ := legacyMigrationFixture(t)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for range 3 {
		data, err := Open(path)
		if data != nil {
			data.Close()
		}
		if !errors.Is(err, store.ErrUnsupportedSchema) {
			t.Fatalf("unsupported schema: %v", err)
		}
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("unsupported database changed: %v", err)
	}
}
