package endpointurl

import (
	"net"
	"strconv"

	"github.com/mishmesh/mishmesh/internal/store"
)

func TLS(endpoint *store.Endpoint, baseDomain string, port int) string {
	host := endpoint.Domain
	if host == "" && endpoint.Subdomain != "" {
		baseHost, _, err := net.SplitHostPort(baseDomain)
		if err != nil {
			baseHost = baseDomain
		}
		host = endpoint.Subdomain + "." + baseHost
	}
	if host == "" {
		return ""
	}
	if port == 0 {
		port = 8444
	}
	return "tls://" + net.JoinHostPort(host, strconv.Itoa(port))
}
