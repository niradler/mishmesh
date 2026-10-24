package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/mishmesh/mishmesh/internal/agent"
	"github.com/mishmesh/mishmesh/internal/config"
)

const logoutRevokeTimeout = 10 * time.Second

func credentialStore() (*agent.CredentialStore, error) {
	path, err := agent.DefaultCredentialsPath()
	if err != nil {
		return nil, err
	}
	return agent.NewCredentialStore(path, nil), nil
}

func resolveServer(explicit, configPath string) (string, error) {
	if explicit != "" {
		return explicit, nil
	}
	gateway := config.LoadAgent().GatewayURL
	if path, err := resolveConfigPath(configPath); err == nil {
		if file, err := agent.LoadConfigFile(path, lenientLookup); err == nil && file.Gateway != "" {
			gateway = file.Gateway
		}
	}
	if gateway == "" {
		return "", errors.New("no server: pass --server or configure a gateway url")
	}
	return agent.APIBaseFromGateway(gateway)
}

func loginCmd(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("login", flag.ContinueOnError)
	server := fs.String("server", "", "control plane base url (default: derived from the gateway url)")
	configPath := fs.String("config", "", "config file used to derive the server")
	org := fs.String("org", "", "organization id to sign in to")
	device := fs.Bool("device", false, "use the device code flow instead of a browser redirect")
	noBrowser := fs.Bool("no-browser", false, "print the sign-in url instead of opening a browser")
	if _, err := parseInterspersed(fs, args); err != nil {
		return err
	}
	base, err := resolveServer(*server, *configPath)
	if err != nil {
		return err
	}
	store, err := credentialStore()
	if err != nil {
		return err
	}
	ctx, stop := signalContext()
	defer stop()
	opts := agent.LoginOptions{Server: base, OrgID: *org, NoBrowser: *noBrowser, Out: out}
	var creds *agent.Credentials
	if *device {
		creds, err = agent.LoginDevice(ctx, opts)
	} else {
		creds, err = agent.LoginBrowser(ctx, opts)
	}
	if err != nil {
		return err
	}
	if err := store.Save(ctx, creds); err != nil {
		return err
	}
	fmt.Fprintf(out, "signed in to %s (org %s); credentials saved to %s\n", creds.Server, creds.OrgID, store.Path())
	return nil
}

func logoutCmd(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("logout", flag.ContinueOnError)
	if _, err := parseInterspersed(fs, args); err != nil {
		return err
	}
	store, err := credentialStore()
	if err != nil {
		return err
	}
	creds, err := store.Load()
	if errors.Is(err, agent.ErrLoginRequired) && creds == nil {
		if _, statErr := os.Stat(store.Path()); errors.Is(statErr, os.ErrNotExist) {
			fmt.Fprintln(out, "not signed in")
			return nil
		}
	}
	if creds != nil {
		ctx, cancel := context.WithTimeout(context.Background(), logoutRevokeTimeout)
		if err := agent.Revoke(ctx, nil, creds.Server, creds.RefreshToken); err != nil {
			fmt.Fprintf(out, "warning: server revocation failed: %v\n", err)
		}
		cancel()
	}
	if err := store.Remove(context.Background()); err != nil {
		return err
	}
	fmt.Fprintln(out, "signed out")
	return nil
}

func statusCmd(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("status", flag.ContinueOnError)
	if _, err := parseInterspersed(fs, args); err != nil {
		return err
	}
	store, err := credentialStore()
	if err != nil {
		return err
	}
	creds, err := store.Load()
	if errors.Is(err, agent.ErrLoginRequired) {
		fmt.Fprintln(out, "not signed in")
		return nil
	}
	if err != nil {
		return err
	}
	state := "valid"
	if !time.Now().Before(creds.ExpiresAt) {
		state = "expired (will refresh on next use)"
	}
	fmt.Fprintf(out, "server:  %s\norg:     %s\naccess:  %s, expires %s\nfile:    %s\n",
		creds.Server, creds.OrgID, state, creds.ExpiresAt.Local().Format(time.RFC3339), store.Path())
	return nil
}
