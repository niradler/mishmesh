package services

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mishmesh/mishmesh/internal/store"
)

func TestShouldDecodeStrictReadiness(t *testing.T) {
	for _, test := range []struct {
		name  string
		raw   string
		want  Readiness
		valid bool
	}{
		{"absent", "", Readiness{}, true},
		{"empty", `{}`, Readiness{}, true},
		{"empty tcp", `{"tcp":""}`, Readiness{}, true},
		{"tcp", `{"tcp":"127.0.0.1:8080"}`, Readiness{TCP: "127.0.0.1:8080"}, true},
		{"http default", `{"http":{"url":"http://localhost/"}}`, Readiness{HTTP: &HTTPReadiness{URL: "http://localhost/", Status: 200}}, true},
		{"https exact", `{"http":{"url":"https://localhost/","status":204}}`, Readiness{HTTP: &HTTPReadiness{URL: "https://localhost/", Status: 204}}, true},
		{"null", `null`, Readiness{}, false},
		{"array", `[]`, Readiness{}, false},
		{"string", `"private-value"`, Readiness{}, false},
		{"unknown", `{"private-value":true}`, Readiness{}, false},
		{"trailing", `{} {}`, Readiness{}, false},
		{"malformed", `{"tcp":`, Readiness{}, false},
		{"duplicate", `{"tcp":"localhost:80","tcp":"localhost:81"}`, Readiness{}, false},
		{"null tcp", `{"tcp":null}`, Readiness{}, false},
		{"number tcp", `{"tcp":80}`, Readiness{}, false},
		{"null http", `{"http":null}`, Readiness{}, false},
		{"array http", `{"http":[]}`, Readiness{}, false},
		{"empty http", `{"http":{}}`, Readiness{}, false},
		{"http unknown", `{"http":{"url":"http://localhost/","private-value":true}}`, Readiness{}, false},
		{"http duplicate", `{"http":{"url":"http://localhost/","status":200,"status":204}}`, Readiness{}, false},
		{"null url", `{"http":{"url":null}}`, Readiness{}, false},
		{"null status", `{"http":{"url":"http://localhost/","status":null}}`, Readiness{}, false},
		{"zero status", `{"http":{"url":"http://localhost/","status":0}}`, Readiness{}, false},
		{"fraction status", `{"http":{"url":"http://localhost/","status":200.5}}`, Readiness{}, false},
		{"string status", `{"http":{"url":"http://localhost/","status":"private-value"}}`, Readiness{}, false},
		{"two modes", `{"tcp":"localhost:80","http":{"url":"http://localhost/"}}`, Readiness{}, false},
		{"empty tcp plus http", `{"tcp":"","http":{"url":"http://localhost/"}}`, Readiness{}, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := DecodeReadiness(json.RawMessage(test.raw))
			if (err == nil) != test.valid {
				t.Fatalf("error=%v valid=%v", err, test.valid)
			}
			if err != nil && strings.Contains(err.Error(), "private-value") {
				t.Fatal("input leaked into error")
			}
			if test.valid && !reflect.DeepEqual(got, test.want) {
				t.Fatalf("got=%+v want=%+v", got, test.want)
			}
		})
	}
}

func TestShouldValidateReadinessTargets(t *testing.T) {
	for _, target := range []string{"localhost", "localhost:http", "localhost:0", "localhost:65536", ":80", "localhost:-1", "localhost:+80", "localhost: 80", "bad host:80", "http://localhost:80", "[not-ip]:80"} {
		if err := ValidateReadiness(Readiness{TCP: target}); err == nil {
			t.Errorf("accepted tcp %q", target)
		}
	}
	for _, target := range []string{"localhost:1", "127.0.0.1:65535", "[::1]:80", "[fe80::1%eth0]:80"} {
		if err := ValidateReadiness(Readiness{TCP: target}); err != nil {
			t.Errorf("rejected tcp %q: %v", target, err)
		}
	}
	for _, target := range []string{"/relative", "ftp://localhost/", "http:///path", "http://localhost:0/", "http://localhost:65536/", "http://bad host/", "http://[not-ip]/", "http:localhost"} {
		if err := ValidateReadiness(Readiness{HTTP: &HTTPReadiness{URL: target, Status: 200}}); err == nil {
			t.Errorf("accepted http %q", target)
		}
	}
	for _, target := range []string{"http://localhost/#fragment", "http://[fe80::1%25eth0]/"} {
		if err := ValidateReadiness(Readiness{HTTP: &HTTPReadiness{URL: target, Status: 200}}); err != nil {
			t.Errorf("rejected http %q: %v", target, err)
		}
	}
	for _, status := range []int{0, 99, 600} {
		if err := ValidateReadiness(Readiness{HTTP: &HTTPReadiness{URL: "http://localhost/", Status: status}}); err == nil {
			t.Errorf("accepted status %d", status)
		}
	}
	if err := ValidateReadiness(Readiness{TCP: "localhost:80", HTTP: &HTTPReadiness{URL: "http://localhost/", Status: 200}}); err == nil {
		t.Fatal("two modes accepted")
	}
}

func TestShouldLoadGenericAndLegacyReadiness(t *testing.T) {
	registry := testRegistry(t)
	for _, test := range []struct {
		ready string
		valid bool
	}{{`{"http":{"url":"http://localhost/"}}`, true}, {`{}`, true}, {`{"tcp":"localhost:http"}`, false}, {`{"unknown":true}`, false}} {
		rec := &store.Service{ID: "svc", Name: "svc", Driver: "docker", Config: json.RawMessage(`{"containers":["web"]}`), Ready: json.RawMessage(test.ready)}
		def, err := FromRecordWithRegistry(rec, registry)
		if (err == nil) != test.valid {
			t.Fatalf("ready=%s error=%v", test.ready, err)
		}
		if test.valid && strings.Contains(test.ready, "http") && (def.Ready.HTTP == nil || def.Ready.HTTP.Status != 200) {
			t.Fatal("HTTP readiness not loaded")
		}
	}
	def, err := FromRecordWithRegistry(&store.Service{ID: "old", Name: "old", Driver: "docker", Config: json.RawMessage(`{"containers":["web"]}`), Ready: json.RawMessage(`{"tcp":"localhost:8080"}`)}, registry)
	if err != nil || def.Ready.TCP != "localhost:8080" {
		t.Fatalf("legacy=%+v error=%v", def, err)
	}
}

type trackingTransport struct {
	base   http.RoundTripper
	closed atomic.Int64
}

type trackingBody struct {
	io.ReadCloser
	closed *atomic.Int64
}

func (b trackingBody) Close() error { b.closed.Add(1); return b.ReadCloser.Close() }
func (t *trackingTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	response, err := t.base.RoundTrip(r)
	if response != nil {
		response.Body = trackingBody{ReadCloser: response.Body, closed: &t.closed}
	}
	return response, err
}

func TestShouldProbeExactHTTPStatusAndCloseBodies(t *testing.T) {
	for _, tls := range []bool{false, true} {
		handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) })
		server := httptest.NewUnstartedServer(handler)
		if tls {
			server.StartTLS()
		} else {
			server.Start()
		}
		t.Cleanup(server.Close)
		transport := &trackingTransport{base: server.Client().Transport}
		if transport.base == nil {
			transport.base = http.DefaultTransport
		}
		client := &http.Client{Transport: transport}
		manager := newManager(Options{HTTPClient: client}, nil)
		if err := manager.probeHTTP(context.Background(), HTTPReadiness{URL: server.URL, Status: 204}); err != nil {
			t.Fatal(err)
		}
		if err := manager.probeHTTP(context.Background(), HTTPReadiness{URL: server.URL, Status: 200}); err == nil {
			t.Fatal("different success status accepted")
		}
		if transport.closed.Load() != 2 {
			t.Fatalf("closed=%d", transport.closed.Load())
		}
	}
}

func TestShouldRefuseHTTPRedirectsWithoutMutatingClient(t *testing.T) {
	var redirected atomic.Int64
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { redirected.Add(1); w.WriteHeader(200) }))
	defer target.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, 302) }))
	defer server.Close()
	var redirectPolicy atomic.Int64
	client := &http.Client{CheckRedirect: func(r *http.Request, via []*http.Request) error { redirectPolicy.Add(1); return nil }}
	manager := newManager(Options{HTTPClient: client}, nil)
	url := strings.Replace(server.URL, "://", "://user:private-value@", 1)
	if err := manager.probeHTTP(context.Background(), HTTPReadiness{URL: url, Status: 200}); err == nil {
		t.Fatal("redirect accepted as 200")
	}
	if err := manager.probeHTTP(context.Background(), HTTPReadiness{URL: url, Status: 302}); err != nil {
		t.Fatal(err)
	}
	if redirected.Load() != 0 || redirectPolicy.Load() != 0 {
		t.Fatal("probe followed redirect")
	}
	response, err := client.Get(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if redirectPolicy.Load() != 1 || redirected.Load() != 1 {
		t.Fatal("caller client policy mutated")
	}
}

func TestShouldBoundAndCancelHTTPProbe(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() }))
	defer server.Close()
	manager := newManager(Options{}, nil)
	for _, duration := range []time.Duration{40 * time.Millisecond, 3 * time.Second} {
		ctx, cancel := context.WithTimeout(context.Background(), duration)
		start := time.Now()
		err := manager.probeHTTP(ctx, HTTPReadiness{URL: server.URL + "/private-value", Status: 200})
		cancel()
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("error=%v", err)
		}
		if strings.Contains(err.Error(), "private-value") {
			t.Fatal("URL leaked")
		}
		if time.Since(start) > 2500*time.Millisecond {
			t.Fatal("probe exceeded per-probe deadline")
		}
	}
}

func TestShouldRetryServiceHTTPReadinessAndIgnoreCallerCancellation(t *testing.T) {
	var requests atomic.Int64
	gate := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if requests.Add(1) == 1 {
			w.WriteHeader(503)
			return
		}
		select {
		case <-gate:
			w.WriteHeader(204)
		case <-r.Context().Done():
		}
	}))
	defer server.Close()
	driver := &fakeDriver{state: Stopped}
	f := newFixture(t, Definition{Ready: Readiness{HTTP: &HTTPReadiness{URL: server.URL, Status: 204}}}, driver)
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	if err := f.manager.EnsureRunning(ctx, "svc", "127.0.0.1:1"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error=%v", err)
	}
	close(gate)
	if err := f.manager.EnsureRunning(context.Background(), "svc", "127.0.0.1:1"); err != nil {
		t.Fatal(err)
	}
	if starts, _ := driver.counts(); starts != 1 {
		t.Fatalf("starts=%d", starts)
	}
	if requests.Load() < 2 || f.dials.Load() != 0 {
		t.Fatal("HTTP readiness did not override endpoint TCP")
	}
}

func TestShouldTimeoutServiceHTTPReadiness(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(503)
		_, _ = w.Write([]byte("private-value"))
	}))
	defer server.Close()
	f := newFixture(t, Definition{StartTimeout: 60 * time.Millisecond, Ready: Readiness{HTTP: &HTTPReadiness{URL: server.URL + "/private-value", Status: 200}}}, &fakeDriver{state: Stopped})
	if err := f.manager.EnsureRunning(context.Background(), "svc", ""); err == nil || strings.Contains(err.Error(), "private-value") {
		t.Fatalf("error=%v", err)
	}
	status, _ := f.manager.Status("svc")
	if status.State != StateError || strings.Contains(status.LastError, "private-value") {
		t.Fatalf("status=%+v", status)
	}
}

func TestShouldCancelTCPReadinessDuringHold(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	accepted := make(chan net.Conn, 1)
	go func() {
		connection, err := listener.Accept()
		if err == nil {
			accepted <- connection
		}
	}()
	manager := newManager(Options{}, nil)
	manager.readyHold = time.Second
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	start := time.Now()
	err = manager.probe(ctx, listener.Addr().String())
	select {
	case connection := <-accepted:
		connection.Close()
	case <-time.After(time.Second):
		t.Fatal("listener did not accept")
	}
	if !errors.Is(err, context.DeadlineExceeded) || time.Since(start) > 300*time.Millisecond {
		t.Fatalf("probe ignored cancellation: error=%v elapsed=%s", err, time.Since(start))
	}
}

func TestShouldSelectGenericTCPAndEmptyReadinessFallbacks(t *testing.T) {
	for _, test := range []struct {
		name     string
		raw      string
		endpoint string
		want     string
	}{
		{"generic wins", `{"tcp":"localhost:8080"}`, "http://localhost:8081/", "localhost:8080"},
		{"empty uses endpoint", `{}`, "http://localhost:8081/path", "localhost:8081"},
		{"absent uses driver", "", "", ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			ready, err := DecodeReadiness(json.RawMessage(test.raw))
			if err != nil {
				t.Fatal(err)
			}
			if got := readinessTarget(Definition{Ready: ready}, test.endpoint); got.TCP != test.want || got.HTTP != nil {
				t.Fatalf("target=%+v want=%s", got, test.want)
			}
		})
	}
}

type readinessErrorConnection struct {
	net.Conn
	deadlineError error
	readError     error
}

func (c readinessErrorConnection) SetReadDeadline(deadline time.Time) error {
	if c.deadlineError != nil {
		return c.deadlineError
	}
	return c.Conn.SetReadDeadline(deadline)
}

func (c readinessErrorConnection) Read(buffer []byte) (int, error) {
	return 0, c.readError
}

func TestShouldSanitizeTCPReadinessTransportErrors(t *testing.T) {
	const privateValue = "private-tcp-transport-value"
	for _, stage := range []string{"dial", "deadline", "read"} {
		for _, cause := range []error{nil, context.Canceled, context.DeadlineExceeded} {
			name := stage
			if cause != nil {
				name += "/" + cause.Error()
			}
			t.Run(name, func(t *testing.T) {
				transportError := errors.New(privateValue)
				if cause != nil {
					transportError = fmt.Errorf("%s: %w", privateValue, cause)
				}
				f := newFixture(t, Definition{StartTimeout: 35 * time.Millisecond, Ready: Readiness{TCP: "localhost:8080"}}, &fakeDriver{state: Stopped})
				var logs bytes.Buffer
				f.manager.log = slog.New(slog.NewTextHandler(&logs, nil))
				f.manager.dial = func(context.Context, string, string) (net.Conn, error) {
					if stage == "dial" {
						return nil, transportError
					}
					client, server := net.Pipe()
					t.Cleanup(func() { client.Close(); server.Close() })
					connection := readinessErrorConnection{Conn: client, readError: transportError}
					if stage == "deadline" {
						connection.deadlineError = transportError
					}
					return connection, nil
				}
				probeError := f.manager.probe(context.Background(), "localhost:8080")
				if probeError == nil || strings.Contains(probeError.Error(), privateValue) || errors.Is(probeError, transportError) {
					t.Errorf("probe exposes raw transport error: %v", probeError)
				}
				if cause != nil && !errors.Is(probeError, cause) {
					t.Errorf("probe lost context cause: %v", probeError)
				}
				ensureError := f.manager.EnsureRunning(context.Background(), "svc", "")
				if ensureError == nil || strings.Contains(ensureError.Error(), privateValue) || errors.Is(ensureError, transportError) {
					t.Errorf("EnsureRunning exposes raw transport error: %v", ensureError)
				}
				status, _ := f.manager.Status("svc")
				if status.State != StateError || status.LastError == "" || strings.Contains(status.LastError, privateValue) {
					t.Errorf("LastError exposes transport detail: %+v", status)
				}
				if logs.Len() == 0 || strings.Contains(logs.String(), privateValue) {
					t.Errorf("log exposes transport detail: %s", logs.String())
				}
			})
		}
	}
}
