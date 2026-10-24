package agent

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/big"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/mishmesh/mishmesh/internal/decryptscope"
	"github.com/mishmesh/mishmesh/internal/tunnel"
)

const (
	maxDecryptLeaves      = 1024
	decryptRetryAfter     = 30 * time.Second
	decryptExpiryMargin   = time.Minute
	decryptLeafBackdate   = 5 * time.Minute
	decryptSerialBits     = 128
	tlsRecordHandshake    = 0x16
	maxEgressCAResponse   = 64 << 10
	egressCAFetchTimeout  = 15 * time.Second
	decryptRefreshDivisor = 3
)

var (
	errDecryptChain    = errors.New("decrypt intermediate: server returned an unusable certificate chain")
	errDecryptLeaf     = errors.New("decrypt leaf: minted certificate does not verify against the org root")
	errDecryptSNI      = errors.New("decrypt: tls server name does not match the CONNECT host")
	errEgressCAInvalid = errors.New("egress ca: server returned an invalid certificate")
)

type decryptState struct {
	key          *ecdsa.PrivateKey
	leafKey      *ecdsa.PrivateKey
	intermediate *x509.Certificate
	roots        *x509.CertPool
	inters       *x509.CertPool
	allow        []string
	bypass       []string

	mu     sync.Mutex
	leaves map[string]*tls.Certificate
	order  []string
}

func (s *decryptState) usable(now time.Time) bool {
	return now.Add(decryptExpiryMargin).Before(s.intermediate.NotAfter) && !now.Before(s.intermediate.NotBefore)
}

func (s *decryptState) leaf(host string, now time.Time) (*tls.Certificate, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if c, ok := s.leaves[host]; ok && now.Add(decryptExpiryMargin).Before(c.Leaf.NotAfter) {
		return c, nil
	}
	c, err := s.mint(host, now)
	if err != nil {
		return nil, err
	}
	if _, ok := s.leaves[host]; !ok {
		if len(s.order) >= maxDecryptLeaves {
			delete(s.leaves, s.order[0])
			s.order = s.order[1:]
		}
		s.order = append(s.order, host)
	}
	s.leaves[host] = c
	return c, nil
}

func (s *decryptState) mint(host string, now time.Time) (*tls.Certificate, error) {
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), decryptSerialBits))
	if err != nil {
		return nil, fmt.Errorf("decrypt leaf serial: %w", err)
	}
	notBefore := now.Add(-decryptLeafBackdate)
	if notBefore.Before(s.intermediate.NotBefore) {
		notBefore = s.intermediate.NotBefore
	}
	tmpl := &x509.Certificate{
		SerialNumber:          serial.Add(serial, big.NewInt(1)),
		Subject:               pkix.Name{CommonName: host},
		DNSNames:              []string{host},
		NotBefore:             notBefore,
		NotAfter:              s.intermediate.NotAfter,
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, s.intermediate, &s.leafKey.PublicKey, s.key)
	if err != nil {
		return nil, fmt.Errorf("decrypt leaf: %w", err)
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, fmt.Errorf("decrypt leaf: %w", err)
	}
	if _, err := leaf.Verify(x509.VerifyOptions{
		DNSName: host, Roots: s.roots, Intermediates: s.inters, CurrentTime: now,
		KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}); err != nil {
		return nil, fmt.Errorf("%w: %w", errDecryptLeaf, err)
	}
	return &tls.Certificate{Certificate: [][]byte{der, s.intermediate.Raw}, PrivateKey: s.leafKey, Leaf: leaf}, nil
}

type decryptor struct {
	dec *remoteDecider
	log *slog.Logger
	now func() time.Time

	mu       sync.Mutex
	gen      uint64
	bound    bool
	state    *decryptState
	disabled bool
	retryAt  time.Time
	inflight chan struct{}
	rerun    bool
	timer    *time.Timer
}

func newDecryptor(dec *remoteDecider, log *slog.Logger, now func() time.Time) *decryptor {
	return &decryptor{dec: dec, log: log, now: now}
}

func (d *decryptor) attach(gen uint64) {
	if d == nil {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	d.gen, d.bound = gen, false
	d.dropLocked()
}

func (d *decryptor) detach(gen uint64) {
	if d == nil {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.gen != gen {
		return
	}
	d.bound = false
	d.dropLocked()
}

func (d *decryptor) bind(gen uint64) {
	if d == nil {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.gen != gen {
		return
	}
	d.bound = true
	d.beginLocked()
}

func (d *decryptor) flush() {
	if d == nil {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	d.dropLocked()
	if d.bound {
		d.beginLocked()
	}
}

func (d *decryptor) dropLocked() {
	d.state, d.disabled, d.retryAt = nil, false, time.Time{}
	if d.inflight != nil {
		d.rerun = true
	}
	if d.timer != nil {
		d.timer.Stop()
		d.timer = nil
	}
}

func (d *decryptor) beginLocked() {
	if d.inflight != nil {
		d.rerun = true
		return
	}
	done := make(chan struct{})
	d.inflight, d.rerun = done, false
	go d.run(d.gen, done)
}

func (d *decryptor) run(gen uint64, done chan struct{}) {
	st, refusal, err := d.issue(gen)
	d.mu.Lock()
	defer d.mu.Unlock()
	current := d.gen == gen && d.bound && !d.rerun
	switch {
	case !current:
	case err != nil:
		d.retryAt = d.now().Add(decryptRetryAfter)
		d.log.Warn("egress decrypt intermediate unavailable; CONNECT stays passthrough", "err", err)
	case refusal != "":
		d.disabled = true
		d.log.Debug("egress decrypt off for this org", "reason", printable(refusal, "refused"))
	default:
		d.state = st
		d.scheduleLocked(gen, st)
		d.log.Info("egress decrypt intermediate issued", "not_after", st.intermediate.NotAfter, "allow", len(st.allow), "bypass", len(st.bypass))
	}
	d.inflight = nil
	close(done)
	if d.rerun && d.gen == gen && d.bound {
		d.beginLocked()
	}
	d.rerun = false
}

func (d *decryptor) scheduleLocked(gen uint64, st *decryptState) {
	if d.timer != nil {
		d.timer.Stop()
	}
	lifetime := st.intermediate.NotAfter.Sub(d.now())
	d.timer = time.AfterFunc(max(lifetime*(decryptRefreshDivisor-1)/decryptRefreshDivisor, time.Second), func() {
		d.mu.Lock()
		defer d.mu.Unlock()
		if d.gen == gen && d.state == st && d.bound {
			d.beginLocked()
		}
	})
}

func (d *decryptor) current(ctx context.Context) *decryptState {
	deadline := time.NewTimer(decisionTimeout)
	defer deadline.Stop()
	for {
		d.mu.Lock()
		if st := d.state; st != nil {
			if st.usable(d.now()) {
				d.mu.Unlock()
				return st
			}
			d.state = nil
		}
		if !d.bound || d.disabled || (d.inflight == nil && d.now().Before(d.retryAt)) {
			d.mu.Unlock()
			return nil
		}
		if d.inflight == nil {
			d.beginLocked()
		}
		wait := d.inflight
		d.mu.Unlock()
		select {
		case <-wait:
		case <-ctx.Done():
			return nil
		case <-deadline.C:
			return nil
		}
	}
}

func (d *decryptor) certFor(ctx context.Context, host string) (*tls.Certificate, string) {
	if d == nil {
		return nil, ""
	}
	canonical, ok := decryptscope.CanonicalHost(host)
	if !ok {
		return nil, ""
	}
	st := d.current(ctx)
	if st == nil || !decryptscope.Decrypts(st.allow, st.bypass, canonical) {
		return nil, ""
	}
	cert, err := st.leaf(canonical, d.now())
	if err != nil {
		d.log.Warn("egress decrypt leaf refused; CONNECT stays passthrough", "host", truncateLog(canonical), "err", err)
		return nil, ""
	}
	return cert, canonical
}

func (d *decryptor) issue(gen uint64) (*decryptState, string, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, "", fmt.Errorf("decrypt key: %w", err)
	}
	csr, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{Subject: pkix.Name{CommonName: "mishmesh egress decrypt"}}, key)
	if err != nil {
		return nil, "", fmt.Errorf("decrypt csr: %w", err)
	}
	resp, err := d.dec.requestCert(context.Background(), gen, csr)
	if err != nil {
		return nil, "", err
	}
	if resp.Error != "" {
		if resp.Error == tunnel.CertErrorDecryptDisabled {
			return nil, resp.Error, nil
		}
		return nil, "", fmt.Errorf("decrypt intermediate: %s", printable(resp.Error, "refused"))
	}
	st, err := d.stateFrom(key, resp)
	if err != nil {
		return nil, "", err
	}
	return st, "", nil
}

func (d *decryptor) stateFrom(key *ecdsa.PrivateKey, resp tunnel.CertResponse) (*decryptState, error) {
	if len(resp.Chain) < 2 {
		return nil, errDecryptChain
	}
	inter, err := x509.ParseCertificate(resp.Chain[0])
	if err != nil {
		return nil, errDecryptChain
	}
	root, err := x509.ParseCertificate(resp.Chain[len(resp.Chain)-1])
	if err != nil {
		return nil, errDecryptChain
	}
	pub, ok := inter.PublicKey.(*ecdsa.PublicKey)
	if !ok || !pub.Equal(&key.PublicKey) || !inter.IsCA || !inter.MaxPathLenZero {
		return nil, errDecryptChain
	}
	roots := x509.NewCertPool()
	roots.AddCert(root)
	if _, err := inter.Verify(x509.VerifyOptions{Roots: roots, CurrentTime: d.now(), KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageAny}}); err != nil {
		return nil, fmt.Errorf("%w: %w", errDecryptChain, err)
	}
	allow, err := decryptscope.Normalize(resp.Allow)
	if err != nil || len(allow) == 0 {
		return nil, errDecryptChain
	}
	bypass, err := decryptscope.Normalize(resp.Bypass)
	if err != nil {
		return nil, errDecryptChain
	}
	leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("decrypt leaf key: %w", err)
	}
	inters := x509.NewCertPool()
	inters.AddCert(inter)
	return &decryptState{
		key: key, leafKey: leafKey, intermediate: inter, roots: roots, inters: inters,
		allow: allow, bypass: bypass, leaves: map[string]*tls.Certificate{},
	}, nil
}

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
	cert, canonical := a.decrypt.certFor(ctx, host)
	cancel()
	if cert == nil {
		return false
	}
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
		a.passthroughAfterConnect(raw, clientIP, dest, creds)
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

func (a *Agent) passthroughAfterConnect(client net.Conn, clientIP, dest string, creds egressCreds) {
	stream, err := a.openEgress(clientIP, dest, creds)
	if err != nil {
		a.log.Info("egress refused", "destination", truncateLog(dest), "err", err)
		return
	}
	defer stream.Close()
	_ = client.SetDeadline(time.Time{})
	tunnel.Splice(client, stream)
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

func FetchEgressCA(ctx context.Context, gatewayURL, token string, client *http.Client) (tunnel.EgressCA, error) {
	gateway, err := NormalizeGatewayURL(gatewayURL)
	if err != nil {
		return tunnel.EgressCA{}, err
	}
	base, err := APIBaseFromGateway(gateway)
	if err != nil {
		return tunnel.EgressCA{}, err
	}
	if client == nil {
		client = &http.Client{Timeout: egressCAFetchTimeout}
	}
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
