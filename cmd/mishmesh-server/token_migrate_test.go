package main

import (
	"context"
	"database/sql"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mishmesh/mishmesh/internal/config"
	"github.com/mishmesh/mishmesh/internal/secrets"
	"github.com/mishmesh/mishmesh/internal/store/migrationtest"
)

func TestShouldCreateTokensAgainstLegacyAndMigratedDatabaseWithoutDaemon(t *testing.T) {
	directory := t.TempDir()
	dsn := filepath.Join(directory, "literal.db")
	key := filepath.Join(directory, "matching.key")
	keyBytes, err := secrets.LoadOrCreateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	box, err := secrets.New(keyBytes)
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	if err := migrationtest.Populate(context.Background(), db, false, box); err != nil {
		t.Fatal(err)
	}
	db.Close()
	t.Setenv("MISHMESH_SECRET_KEY_FILE", key)
	before, err := os.ReadFile(key)
	if err != nil {
		t.Fatal(err)
	}
	identity := ""
	for _, name := range []string{"first", "reopened"} {
		reader, writer, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		original := os.Stdout
		os.Stdout = writer
		err = tokenCmd([]string{"create", "--dsn", dsn, "--org", "Imported network", "--name", name})
		os.Stdout = original
		writer.Close()
		output, readErr := io.ReadAll(reader)
		reader.Close()
		if err != nil || readErr != nil {
			t.Fatalf("token create failed %v %v", err, readErr)
		}
		if !strings.Contains(string(output), "token:") || strings.Contains(string(output), migrationtest.TunnelToken) || strings.Contains(string(output), migrationtest.APIToken) {
			t.Fatal("token output missing result or leaked connection credentials")
		}
		data, err := openDataStore(config.Server{DataDSN: dsn, SecretKeyFile: key})
		if err != nil {
			t.Fatal(err)
		}
		current, err := migrationtest.AssertConverted(context.Background(), data, box)
		if err != nil {
			data.Close()
			t.Fatal(err)
		}
		if identity != "" && current != identity {
			t.Fatal("token reopen changed connection identity")
		}
		identity = current
		data.Close()
	}
	after, err := os.ReadFile(key)
	if err != nil || string(after) != string(before) {
		t.Fatalf("token command changed existing key %v", err)
	}
}
