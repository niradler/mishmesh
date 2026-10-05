package agent

import (
	"fmt"
	"strings"

	"github.com/mishmesh/mishmesh/internal/tunnel"
)

type TunnelResult struct {
	Name        string
	Kind        string
	LocalTarget string
	Binding     tunnel.EndpointBinding
}

func FormatResults(gatewayURL string, results []TunnelResult) string {
	nameWidth, kindWidth := 0, 0
	for _, r := range results {
		nameWidth = max(nameWidth, len(r.Name))
		kindWidth = max(kindWidth, len(r.Kind))
	}
	var b strings.Builder
	fmt.Fprintf(&b, "\nmishmesh agent connected to %s\n\n", gatewayURL)
	indent := strings.Repeat(" ", 2+nameWidth+2+kindWidth+2)
	for _, r := range results {
		head := fmt.Sprintf("  %-*s  %-*s  %s", nameWidth, r.Name, kindWidth, r.Kind, r.LocalTarget)
		if r.Binding.EndpointID == "" {
			reason := r.Binding.Error
			if reason == "" {
				reason = "rejected by the gateway without a reason (gateway too old to report one)"
			}
			fmt.Fprintf(&b, "%s  FAILED: %s\n", head, reason)
			continue
		}
		fmt.Fprintf(&b, "%s  ->  %s\n", head, r.Binding.PublicURL)
		fmt.Fprintf(&b, "%sendpoint: %s\n", indent, r.Binding.EndpointID)
		if r.Binding.PathURL != "" {
			fmt.Fprintf(&b, "%spath:     %s\n", indent, r.Binding.PathURL)
		}
	}
	b.WriteString("\n")
	return b.String()
}
