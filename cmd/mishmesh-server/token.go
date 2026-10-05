package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"time"

	"github.com/mishmesh/mishmesh/internal/config"
	"github.com/mishmesh/mishmesh/internal/store"
)

type tokenOptions struct {
	dsn       string
	orgName   string
	agentName string
}

type tokenResult struct {
	org        *store.Org
	orgCreated bool
	agent      *store.Agent
	rawToken   string
}

func tokenCmd(args []string) error {
	opts, err := parseTokenArgs(args)
	if err != nil {
		return err
	}
	cfg := config.LoadServer()
	cfg.DataDSN = opts.dsn
	cfg.DataBackend = ""
	data, err := openDataStore(cfg)
	if err != nil {
		return err
	}
	defer data.Close()

	res, err := createToken(context.Background(), data, opts)
	if err != nil {
		return err
	}
	printTokenResult(res)
	return nil
}

func parseTokenArgs(args []string) (tokenOptions, error) {
	cfg := config.LoadServer()
	opts := tokenOptions{dsn: cfg.DataDSN}
	fs := flag.NewFlagSet("token create", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&opts.dsn, "dsn", opts.dsn, "")
	fs.StringVar(&opts.orgName, "org", "default", "")
	fs.StringVar(&opts.agentName, "name", "agent", "")

	var sub string
	rest := args
	for {
		if err := fs.Parse(rest); err != nil {
			return opts, fmt.Errorf("token create: %w", err)
		}
		if fs.NArg() == 0 {
			break
		}
		if sub != "" {
			return opts, fmt.Errorf("token create: unexpected argument %q", fs.Arg(0))
		}
		sub = fs.Arg(0)
		rest = fs.Args()[1:]
	}
	if sub != "create" {
		return opts, errors.New("usage: mishmesh-server token create [--org NAME] [--name NAME] [--dsn DSN]")
	}
	if opts.orgName == "" || opts.agentName == "" {
		return opts, errors.New("token create: --org and --name must not be empty")
	}
	return opts, nil
}

func createToken(ctx context.Context, data store.DataStore, opts tokenOptions) (*tokenResult, error) {
	now := time.Now()
	org, created, err := findOrCreateOrg(ctx, data, opts.orgName, now)
	if err != nil {
		return nil, err
	}
	agent := &store.Agent{ID: store.NewID("ag"), OrgID: org.ID, Name: opts.agentName, Status: store.AgentActive, CreatedAt: now}
	if err := data.CreateAgent(ctx, agent); err != nil {
		return nil, fmt.Errorf("create agent: %w", err)
	}
	raw, hash, err := store.GenerateToken()
	if err != nil {
		return nil, err
	}
	tok := &store.Token{ID: store.NewID("tok"), OrgID: org.ID, AgentID: agent.ID, Hash: hash, CreatedAt: now}
	if err := data.CreateToken(ctx, tok); err != nil {
		return nil, fmt.Errorf("create token: %w", err)
	}
	return &tokenResult{org: org, orgCreated: created, agent: agent, rawToken: raw}, nil
}

func findOrCreateOrg(ctx context.Context, data store.DataStore, name string, now time.Time) (*store.Org, bool, error) {
	orgs, err := data.ListOrgs(ctx)
	if err != nil {
		return nil, false, fmt.Errorf("list orgs: %w", err)
	}
	for _, o := range orgs {
		if o.Name == name {
			return o, false, nil
		}
	}
	org := &store.Org{ID: store.NewID("org"), Name: name, CreatedAt: now}
	if err := data.CreateOrg(ctx, org); err != nil {
		return nil, false, fmt.Errorf("create org: %w", err)
	}
	return org, true, nil
}

func printTokenResult(res *tokenResult) {
	orgState := "existing"
	if res.orgCreated {
		orgState = "created"
	}
	fmt.Printf("org:      %s (%s, %s)\n", res.org.Name, res.org.ID, orgState)
	fmt.Printf("agent:    %s (%s)\n", res.agent.Name, res.agent.ID)
	fmt.Printf("token:    %s\n", res.rawToken)
	fmt.Println("\nthe token is shown once. run the agent with:")
	fmt.Printf("  MISHMESH_TOKEN=%s mishmesh-agent http 3000\n", res.rawToken)
}
