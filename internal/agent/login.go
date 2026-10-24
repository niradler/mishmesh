package agent

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"html"
	"io"
	"net"
	"net/http"
	"net/url"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

const (
	loginTimeout     = 5 * time.Minute
	deviceSlowDown   = 5 * time.Second
	minDeviceBackoff = time.Second
	grantDeviceCode  = "urn:ietf:params:oauth:grant-type:device_code"
)

type LoginOptions struct {
	Server    string
	OrgID     string
	NoBrowser bool
	Out       io.Writer
	Client    *http.Client
	Now       func() time.Time
	Open      func(url string) error
}

func (o *LoginOptions) defaults() {
	o.Server = strings.TrimRight(o.Server, "/")
	if o.Client == nil {
		o.Client = &http.Client{Timeout: tokenRequestTimeout}
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Open == nil {
		o.Open = OpenBrowser
	}
	if o.Out == nil {
		o.Out = io.Discard
	}
}

func APIBaseFromGateway(gateway string) (string, error) {
	u, err := url.Parse(gateway)
	if err != nil || u.Host == "" {
		return "", fmt.Errorf("invalid gateway url %q", gateway)
	}
	switch u.Scheme {
	case "ws", "http":
		u.Scheme = "http"
	case "wss", "https":
		u.Scheme = "https"
	default:
		return "", fmt.Errorf("invalid gateway url scheme %q", u.Scheme)
	}
	return u.Scheme + "://" + u.Host, nil
}

func LoginBrowser(ctx context.Context, o LoginOptions) (*Credentials, error) {
	o.defaults()
	ctx, cancel := context.WithTimeout(ctx, loginTimeout)
	defer cancel()

	verifier, err := randomToken(32)
	if err != nil {
		return nil, err
	}
	state, err := randomToken(16)
	if err != nil {
		return nil, err
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("start login listener: %w", err)
	}
	defer ln.Close()
	redirectURI := "http://" + ln.Addr().String() + "/callback"

	type callback struct {
		code string
		err  error
	}
	results := make(chan callback, 1)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /callback", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		var res callback
		switch {
		case subtle.ConstantTimeCompare([]byte(q.Get("state")), []byte(state)) != 1:
			res.err = errors.New("login callback state mismatch")
		case q.Get("error") != "":
			res.err = fmt.Errorf("login refused: %s", q.Get("error"))
		case q.Get("code") == "":
			res.err = errors.New("login callback missing code")
		default:
			res.code = q.Get("code")
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		if res.err != nil {
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprintf(w, "<p>mishmesh login failed: %s</p>", html.EscapeString(res.err.Error()))
		} else {
			fmt.Fprint(w, "<p>mishmesh login complete. You can close this window.</p>")
		}
		select {
		case results <- res:
		default:
		}
	})
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = srv.Serve(ln) }()
	defer srv.Close()

	q := url.Values{
		"response_type":         {"code"},
		"client_id":             {AgentClientID},
		"redirect_uri":          {redirectURI},
		"state":                 {state},
		"code_challenge":        {pkceChallenge(verifier)},
		"code_challenge_method": {"S256"},
	}
	if o.OrgID != "" {
		q.Set("org_id", o.OrgID)
	}
	authURL := o.Server + "/oauth2/authorize?" + q.Encode()
	fmt.Fprintf(o.Out, "Open this URL to sign in:\n  %s\n", authURL)
	if !o.NoBrowser {
		if err := o.Open(authURL); err != nil {
			fmt.Fprintf(o.Out, "could not open a browser: %v\n", err)
		}
	}

	var res callback
	select {
	case res = <-results:
	case <-ctx.Done():
		return nil, fmt.Errorf("waiting for login: %w", ctx.Err())
	}
	if res.err != nil {
		return nil, res.err
	}
	form := url.Values{
		"grant_type":    {"authorization_code"},
		"client_id":     {AgentClientID},
		"code":          {res.code},
		"code_verifier": {verifier},
		"redirect_uri":  {redirectURI},
	}
	return exchange(ctx, o, form)
}

type deviceStart struct {
	DeviceCode              string `json:"device_code"`
	UserCode                string `json:"user_code"`
	VerificationURI         string `json:"verification_uri"`
	VerificationURIComplete string `json:"verification_uri_complete"`
	ExpiresIn               int    `json:"expires_in"`
	Interval                int    `json:"interval"`
}

func LoginDevice(ctx context.Context, o LoginOptions) (*Credentials, error) {
	o.defaults()
	var start deviceStart
	status, err := postJSON(ctx, o.Client, o.Server+"/oauth2/device_authorization", url.Values{"client_id": {AgentClientID}}, &start)
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK || start.DeviceCode == "" {
		return nil, fmt.Errorf("device authorization failed: status %d", status)
	}
	fmt.Fprintf(o.Out, "To sign in, visit %s and enter code %s\n", start.VerificationURI, start.UserCode)
	if start.VerificationURIComplete != "" {
		fmt.Fprintf(o.Out, "or open %s\n", start.VerificationURIComplete)
	}
	expires := time.Duration(max(start.ExpiresIn, 1)) * time.Second
	ctx, cancel := context.WithTimeout(ctx, expires)
	defer cancel()
	interval := max(time.Duration(start.Interval)*time.Second, minDeviceBackoff)
	form := url.Values{"grant_type": {grantDeviceCode}, "client_id": {AgentClientID}, "device_code": {start.DeviceCode}}
	for {
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("device login expired: %w", ctx.Err())
		case <-time.After(interval):
		}
		resp, status, err := postToken(ctx, o.Client, o.Server, form)
		if err != nil {
			return nil, err
		}
		if status == http.StatusOK {
			return credentialsFrom(o.Server, resp, o.Now())
		}
		switch resp.Error {
		case "authorization_pending":
		case "slow_down":
			interval += deviceSlowDown
		case "expired_token":
			return nil, errors.New("device code expired: run login again")
		case "access_denied":
			return nil, errors.New("device login was denied")
		default:
			return nil, fmt.Errorf("device login failed: status %d %s", status, resp.Error)
		}
	}
}

func Revoke(ctx context.Context, client *http.Client, server, token string) error {
	if client == nil {
		client = &http.Client{Timeout: tokenRequestTimeout}
	}
	_, status, err := postForm(ctx, client, strings.TrimRight(server, "/")+"/oauth2/revoke", url.Values{"client_id": {AgentClientID}, "token": {token}})
	if err != nil {
		return err
	}
	if status != http.StatusOK {
		return fmt.Errorf("revoke failed: status %d", status)
	}
	return nil
}

func exchange(ctx context.Context, o LoginOptions, form url.Values) (*Credentials, error) {
	resp, status, err := postToken(ctx, o.Client, o.Server, form)
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("token exchange failed: status %d %s", status, resp.Error)
	}
	return credentialsFrom(o.Server, resp, o.Now())
}

func postJSON(ctx context.Context, client *http.Client, endpoint string, form url.Values, out *deviceStart) (int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return 0, fmt.Errorf("contact %s: %w", endpoint, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return resp.StatusCode, nil
	}
	if err := decodeLimited(resp.Body, out); err != nil {
		return resp.StatusCode, fmt.Errorf("decode device response: %w", err)
	}
	return resp.StatusCode, nil
}

func OpenBrowser(target string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", target)
	case "darwin":
		cmd = exec.Command("open", target)
	default:
		cmd = exec.Command("xdg-open", target)
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() { _ = cmd.Wait() }()
	return nil
}

func randomToken(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate random: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func pkceChallenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}
