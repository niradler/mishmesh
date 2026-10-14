package main

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestShouldCreateTokensAgainstFreshTypedDatabaseWithoutDaemon(t *testing.T) {
	directory := t.TempDir()
	dsn := filepath.Join(directory, "typed.db")
	key := filepath.Join(directory, "matching.key")
	t.Setenv("MISHMESH_SECRET_KEY_FILE", key)
	for _, name := range []string{"first", "reopened"} {
		reader, writer, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		original := os.Stdout
		os.Stdout = writer
		err = tokenCmd([]string{"create", "--dsn", dsn, "--org", "Typed network", "--name", name})
		os.Stdout = original
		writer.Close()
		output, readErr := io.ReadAll(reader)
		reader.Close()
		if err != nil || readErr != nil || !strings.Contains(string(output), "token:") {
			t.Fatalf("token create: %v %v %s", err, readErr, output)
		}
	}
}
