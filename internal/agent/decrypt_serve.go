package agent

import (
	"bufio"
	"context"
	"crypto/tls"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"
)

func (a *Agent) serveDecrypted(client net.Conn, br *bufio.Reader, dest string, creds egressCreds) bool {
	host, portText, err := net.SplitHostPort(dest)
	if err != nil {
		return false
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port < 1 || port > 65535 {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), egressHandshakeTimeout)
	plan := a.decrypt.classify(ctx, host)
	cancel()
	switch plan.verdict {
	case decryptPassthrough:
		return false
	case decryptRefuse:
		writeProxyError(client, http.StatusServiceUnavailable, errDecryptUnavailable.Error())
		return true
	}
	cert, canonical := plan.cert, plan.canonical
	if _, err := io.WriteString(client, "HTTP/1.1 200 Connection Established\r\n\r\n"); err != nil {
		return true
	}
	clientIP := remoteHost(client.RemoteAddr().String())
	_ = client.SetDeadline(time.Now().Add(egressHandshakeTimeout))
	first, err := br.Peek(1)
	if err != nil {
		return true
	}
	raw := &bufferedConn{Conn: client, r: br}
	if first[0] != tlsRecordHandshake {
		a.log.Info("egress refused: decrypt-scoped CONNECT did not start with tls", "destination", truncateLog(dest))
		return true
	}
	ln := &oneConnListener{done: make(chan struct{})}
	conn := tls.Server(&notifyCloseConn{Conn: raw, closed: ln.done}, decryptTLSConfig(cert, canonical))
	ln.conn = conn
	if err := conn.Handshake(); err != nil {
		a.log.Info("egress decrypt handshake failed", "destination", truncateLog(dest), "err", err)
		return true
	}
	_ = client.SetDeadline(time.Time{})
	target := originTarget{scheme: "https", host: canonical, port: port}
	srv := a.origin.server(a.origin.decryptedHandler(target, creds, clientIP))
	_ = srv.Serve(ln)
	return true
}

func (o *originator) decryptedHandler(target originTarget, creds egressCreds, clientIP string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodConnect || isConnectUDP(r) {
			http.Error(w, "egress refused: tunnels inside a decrypted connection are not allowed", http.StatusMethodNotAllowed)
			return
		}
		o.serve(w, r, target, creds, clientIP, true)
	})
}

func decryptTLSConfig(cert *tls.Certificate, host string) *tls.Config {
	return &tls.Config{
		MinVersion: tls.VersionTLS12,
		NextProtos: []string{"h2", "http/1.1"},
		GetCertificate: func(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
			if hello.ServerName != "" && !strings.EqualFold(strings.TrimSuffix(hello.ServerName, "."), host) {
				return nil, errDecryptSNI
			}
			return cert, nil
		},
	}
}
