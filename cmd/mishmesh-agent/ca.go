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

const caFetchTimeout = 20 * time.Second

func caCmd(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("ca", flag.ContinueOnError)
	configPath := fs.String("config", "", "config file")
	gateway := fs.String("gateway", "", "gateway URL, overrides the config file")
	token := fs.String("token", "", "agent authtoken, overrides the config file")
	if _, err := parseInterspersed(fs, args); err != nil {
		return err
	}
	gatewayURL, agentToken, err := caCredentials(*configPath, *gateway, *token)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), caFetchTimeout)
	defer cancel()
	ca, err := agent.FetchEgressCA(ctx, gatewayURL, agentToken, nil)
	if err != nil {
		return err
	}
	fmt.Fprint(out, ca.PEM)
	fmt.Fprintf(os.Stderr, "sha256 fingerprint: %s\nbefore trusting it, compare with the sha256 from the admin API: GET /api/v1/egress/ca\n", ca.SHA256)
	return nil
}

func caCredentials(configPath, gateway, token string) (string, string, error) {
	env := config.LoadAgent()
	var file *agent.FileConfig
	if path, err := resolveConfigPath(configPath); err == nil {
		if file, err = agent.LoadConfigFile(path, os.LookupEnv); err != nil {
			return "", "", err
		}
	} else if configPath != "" || gateway == "" && env.GatewayURL == "" {
		return "", "", err
	}
	var fileGateway, fileToken string
	if file != nil {
		fileGateway, fileToken = file.Gateway, file.Token
	}
	gatewayURL := firstNonEmpty(gateway, fileGateway, env.GatewayURL)
	agentToken := firstNonEmpty(token, fileToken, env.Token)
	if gatewayURL == "" || agentToken == "" {
		return "", "", errors.New("ca needs a gateway URL and an agent token: use a config file, --gateway/--token, or MISHMESH_GATEWAY_URL/MISHMESH_TOKEN")
	}
	return gatewayURL, agentToken, nil
}
