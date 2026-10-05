package main

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/mishmesh/mishmesh/internal/store"
	"github.com/mishmesh/mishmesh/internal/store/sqlite"
)

func TestParseTokenArgs(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		want    tokenOptions
		wantErr bool
	}{
		{name: "flags after create", args: []string{"create", "--org", "acme", "--name", "laptop", "--dsn", "x.db"}, want: tokenOptions{dsn: "x.db", orgName: "acme", agentName: "laptop"}},
		{name: "flags before create", args: []string{"--org", "acme", "create"}, want: tokenOptions{dsn: "mishmesh.db", orgName: "acme", agentName: "agent"}},
		{name: "defaults", args: []string{"create"}, want: tokenOptions{dsn: "mishmesh.db", orgName: "default", agentName: "agent"}},
		{name: "equals syntax", args: []string{"create", "--org=acme"}, want: tokenOptions{dsn: "mishmesh.db", orgName: "acme", agentName: "agent"}},
		{name: "missing subcommand", args: []string{"--org", "acme"}, wantErr: true},
		{name: "unknown subcommand", args: []string{"revoke"}, wantErr: true},
		{name: "extra positional", args: []string{"create", "extra"}, wantErr: true},
		{name: "unknown flag", args: []string{"create", "--bogus"}, wantErr: true},
		{name: "empty org", args: []string{"create", "--org", ""}, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("MISHMESH_DATA_DSN", "mishmesh.db")
			got, err := parseTokenArgs(tt.args)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if !tt.wantErr && got != tt.want {
				t.Fatalf("got %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestCreateTokenReusesOrgByName(t *testing.T) {
	ctx := context.Background()
	data, err := sqlite.Open(filepath.Join(t.TempDir(), "tok.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = data.Close() })

	first, err := createToken(ctx, data, tokenOptions{orgName: "acme", agentName: "a1"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := createToken(ctx, data, tokenOptions{orgName: "acme", agentName: "a2"})
	if err != nil {
		t.Fatal(err)
	}
	other, err := createToken(ctx, data, tokenOptions{orgName: "other", agentName: "a3"})
	if err != nil {
		t.Fatal(err)
	}
	if !first.orgCreated || second.orgCreated || !other.orgCreated {
		t.Fatalf("orgCreated = %v/%v/%v", first.orgCreated, second.orgCreated, other.orgCreated)
	}
	if first.org.ID != second.org.ID || first.org.ID == other.org.ID {
		t.Fatalf("org ids: %s %s %s", first.org.ID, second.org.ID, other.org.ID)
	}
	if first.org.Name != "acme" || second.agent.Name != "a2" {
		t.Fatalf("names: org=%q agent=%q", first.org.Name, second.agent.Name)
	}
	tok, err := data.GetTokenByHash(ctx, store.HashToken(second.rawToken))
	if err != nil || tok.AgentID != second.agent.ID {
		t.Fatalf("token lookup: %v %+v", err, tok)
	}
}
