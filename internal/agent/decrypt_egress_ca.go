package agent

import (
	"context"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/mishmesh/mishmesh/internal/tunnel"
)

func FetchEgressCA(ctx context.Context, gatewayURL, token string, client *http.Client) (tunnel.EgressCA, error) {
	gateway, err := NormalizeGatewayURL(gatewayURL)
	if err != nil {
		return tunnel.EgressCA{}, err
	}
	base, err := APIBaseFromGateway(gateway)
	if err != nil {
		return tunnel.EgressCA{}, err
	}
	client = httpClientOrDefault(client, egressCAFetchTimeout)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+tunnel.AgentEgressCAPath, nil)
	if err != nil {
		return tunnel.EgressCA{}, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := client.Do(req)
	if err != nil {
		return tunnel.EgressCA{}, fmt.Errorf("fetch egress ca: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxEgressCAResponse))
	if err != nil {
		return tunnel.EgressCA{}, fmt.Errorf("fetch egress ca: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return tunnel.EgressCA{}, fmt.Errorf("fetch egress ca: %s: %s", resp.Status, printable(strings.TrimSpace(string(body)), "no detail"))
	}
	var ca tunnel.EgressCA
	if err := json.Unmarshal(body, &ca); err != nil {
		return tunnel.EgressCA{}, errEgressCAInvalid
	}
	block, _ := pem.Decode([]byte(ca.PEM))
	if block == nil || block.Type != "CERTIFICATE" {
		return tunnel.EgressCA{}, errEgressCAInvalid
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil || !cert.IsCA {
		return tunnel.EgressCA{}, errEgressCAInvalid
	}
	sum := sha256.Sum256(block.Bytes)
	if fingerprint := hex.EncodeToString(sum[:]); !strings.EqualFold(fingerprint, ca.SHA256) {
		return tunnel.EgressCA{}, errEgressCAInvalid
	}
	return ca, nil
}
