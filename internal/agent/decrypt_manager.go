package agent

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/mishmesh/mishmesh/internal/decryptscope"
	"github.com/mishmesh/mishmesh/internal/tunnel"
)

const (
	maxDecryptLeaves      = 1024
	decryptRetryAfter     = 5 * time.Second
	decryptExpiryMargin   = time.Minute
	decryptLeafBackdate   = 5 * time.Minute
	decryptSerialBits     = 128
	maxEgressCAResponse   = 64 << 10
	egressCAFetchTimeout  = 15 * time.Second
	decryptRefreshDivisor = 3
)

var (
	errDecryptChain       = errors.New("decrypt intermediate: server returned an unusable certificate chain")
	errDecryptLeaf        = errors.New("decrypt leaf: minted certificate does not verify against the org root")
	errDecryptUnavailable = errors.New("egress decryption unavailable")
	errDecryptSNI         = errors.New("decrypt: tls server name does not match the CONNECT host")
	errEgressCAInvalid    = errors.New("egress ca: server returned an invalid certificate")
)

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
		d.log.Warn("egress decrypt intermediate unavailable; CONNECT refused until it is issued", "err", err)
	case refusal != "":
		d.disabled = true
		d.log.Debug("egress decrypt off for this org", "reason", printable(refusal, "refused"))
	default:
		d.state = st
		d.scheduleLocked(gen, st)
		d.log.Info("egress decrypt intermediate issued", "not_after", st.notAfter, "intermediates", len(st.signers), "allow", len(st.allow), "bypass", len(st.bypass))
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
	lifetime := st.notAfter.Sub(d.now())
	d.timer = time.AfterFunc(max(lifetime*(decryptRefreshDivisor-1)/decryptRefreshDivisor, time.Second), func() {
		d.mu.Lock()
		defer d.mu.Unlock()
		if d.gen == gen && d.state == st && d.bound {
			d.beginLocked()
		}
	})
}

func (d *decryptor) current(ctx context.Context) (*decryptState, bool) {
	deadline := time.NewTimer(decisionTimeout)
	defer deadline.Stop()
	for {
		d.mu.Lock()
		if st := d.state; st != nil {
			if st.usable(d.now()) {
				d.mu.Unlock()
				return st, true
			}
			d.state = nil
		}
		if d.disabled {
			d.mu.Unlock()
			return nil, true
		}
		if !d.bound || (d.inflight == nil && d.now().Before(d.retryAt)) {
			d.mu.Unlock()
			return nil, false
		}
		if d.inflight == nil {
			d.beginLocked()
		}
		wait := d.inflight
		d.mu.Unlock()
		select {
		case <-wait:
		case <-ctx.Done():
			return nil, false
		case <-deadline.C:
			return nil, false
		}
	}
}

type decryptVerdict int

const (
	decryptPassthrough decryptVerdict = iota
	decryptTerminate
	decryptRefuse
)

type decryptPlan struct {
	verdict   decryptVerdict
	cert      *tls.Certificate
	canonical string
}

func (d *decryptor) classify(ctx context.Context, host string) decryptPlan {
	if d == nil {
		return decryptPlan{verdict: decryptPassthrough}
	}
	canonical, ok := decryptscope.CanonicalHost(host)
	if !ok {
		return decryptPlan{verdict: decryptPassthrough}
	}
	st, known := d.current(ctx)
	switch {
	case !known:
		d.log.Warn("egress decrypt scope unknown; CONNECT refused", "host", truncateLog(canonical))
		return decryptPlan{verdict: decryptRefuse}
	case st == nil || !decryptscope.Decrypts(st.allow, st.bypass, canonical):
		return decryptPlan{verdict: decryptPassthrough}
	}
	cert, err := st.leaf(canonical, d.now())
	if err != nil {
		d.log.Warn("egress decrypt leaf unavailable; CONNECT refused", "host", truncateLog(canonical), "err", err)
		return decryptPlan{verdict: decryptRefuse}
	}
	return decryptPlan{verdict: decryptTerminate, cert: cert, canonical: canonical}
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
	if len(resp.Chain) < 2 || len(resp.Extra) >= decryptscope.MaxPatterns {
		return nil, errDecryptChain
	}
	root, err := x509.ParseCertificate(resp.Chain[len(resp.Chain)-1])
	if err != nil {
		return nil, errDecryptChain
	}
	roots := x509.NewCertPool()
	roots.AddCert(root)
	st := &decryptState{key: key, roots: roots, leaves: map[string]*tls.Certificate{}}
	for _, der := range append([][]byte{resp.Chain[0]}, resp.Extra...) {
		inter, err := d.verifiedIntermediate(key, roots, der)
		if err != nil {
			return nil, err
		}
		pool := x509.NewCertPool()
		pool.AddCert(inter)
		st.signers = append(st.signers, decryptSigner{cert: inter, pool: pool})
		if st.notBefore.IsZero() || inter.NotBefore.After(st.notBefore) {
			st.notBefore = inter.NotBefore
		}
		if st.notAfter.IsZero() || inter.NotAfter.Before(st.notAfter) {
			st.notAfter = inter.NotAfter
		}
	}
	if st.allow, err = decryptscope.Normalize(resp.Allow); err != nil || len(st.allow) == 0 {
		return nil, errDecryptChain
	}
	if st.bypass, err = decryptscope.Normalize(resp.Bypass); err != nil {
		return nil, errDecryptChain
	}
	if st.leafKey, err = ecdsa.GenerateKey(elliptic.P256(), rand.Reader); err != nil {
		return nil, fmt.Errorf("decrypt leaf key: %w", err)
	}
	return st, nil
}

func (d *decryptor) verifiedIntermediate(key *ecdsa.PrivateKey, roots *x509.CertPool, der []byte) (*x509.Certificate, error) {
	inter, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, errDecryptChain
	}
	pub, ok := inter.PublicKey.(*ecdsa.PublicKey)
	if !ok || !pub.Equal(&key.PublicKey) || !inter.IsCA || !inter.MaxPathLenZero {
		return nil, errDecryptChain
	}
	if _, err := inter.Verify(x509.VerifyOptions{Roots: roots, CurrentTime: d.now(), KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageAny}}); err != nil {
		return nil, fmt.Errorf("%w: %w", errDecryptChain, err)
	}
	return inter, nil
}
