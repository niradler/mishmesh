package services

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeContainer struct {
	ID     string
	Name   string
	State  string
	Labels map[string]string
}

type fakeEngine struct {
	t          *testing.T
	server     *httptest.Server
	mu         sync.Mutex
	version    string
	containers []*fakeContainer
	requests   []string
	failStart  map[string]string
	failList   bool
}

func newFakeEngine(t *testing.T, containers ...*fakeContainer) *fakeEngine {
	t.Helper()
	e := &fakeEngine{t: t, version: "1.43", containers: containers, failStart: map[string]string{}}
	e.server = httptest.NewServer(http.HandlerFunc(e.handle))
	t.Cleanup(e.server.Close)
	return e
}

func (e *fakeEngine) host() string {
	return "tcp://" + e.server.Listener.Addr().String()
}

func (e *fakeEngine) client() *dockerClient {
	e.t.Helper()
	c, err := newDockerClient(e.host())
	if err != nil {
		e.t.Fatal(err)
	}
	return c
}

func (e *fakeEngine) requestLog() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.requests...)
}

func (e *fakeEngine) stateOf(id string) string {
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, c := range e.containers {
		if c.ID == id {
			return c.State
		}
	}
	return ""
}

func (e *fakeEngine) writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func (e *fakeEngine) handle(w http.ResponseWriter, r *http.Request) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.requests = append(e.requests, r.Method+" "+r.URL.Path)

	if r.URL.Path == "/version" {
		if e.version == "" {
			e.writeJSON(w, http.StatusInternalServerError, map[string]string{"message": "version unavailable"})
			return
		}
		e.writeJSON(w, http.StatusOK, map[string]string{"ApiVersion": e.version})
		return
	}
	prefix := "/v" + e.version
	if !strings.HasPrefix(r.URL.Path, prefix+"/") {
		e.writeJSON(w, http.StatusNotFound, map[string]string{"message": "page not found"})
		return
	}
	path := strings.TrimPrefix(r.URL.Path, prefix)

	switch {
	case r.Method == http.MethodGet && path == "/containers/json":
		if e.failList {
			e.writeJSON(w, http.StatusInternalServerError, map[string]string{"message": "list exploded"})
			return
		}
		if r.URL.Query().Get("all") != "1" {
			e.writeJSON(w, http.StatusBadRequest, map[string]string{"message": "all=1 required"})
			return
		}
		var filters map[string][]string
		if err := json.Unmarshal([]byte(r.URL.Query().Get("filters")), &filters); err != nil {
			e.writeJSON(w, http.StatusBadRequest, map[string]string{"message": "bad filters"})
			return
		}
		out := []map[string]any{}
		for _, c := range e.containers {
			if !matchFilters(c, filters) {
				continue
			}
			out = append(out, map[string]any{"Id": c.ID, "Names": []string{"/" + c.Name}, "State": c.State})
		}
		e.writeJSON(w, http.StatusOK, out)
	case r.Method == http.MethodPost && strings.HasPrefix(path, "/containers/"):
		parts := strings.Split(strings.TrimPrefix(path, "/containers/"), "/")
		if len(parts) != 2 {
			e.writeJSON(w, http.StatusNotFound, map[string]string{"message": "bad path"})
			return
		}
		e.handleAction(w, r, parts[0], parts[1])
	default:
		e.writeJSON(w, http.StatusNotFound, map[string]string{"message": "page not found"})
	}
}

func (e *fakeEngine) handleAction(w http.ResponseWriter, r *http.Request, id, action string) {
	var target *fakeContainer
	for _, c := range e.containers {
		if c.ID == id {
			target = c
		}
	}
	if target == nil {
		e.writeJSON(w, http.StatusNotFound, map[string]string{"message": "No such container: " + id})
		return
	}
	switch action {
	case "start":
		if msg, ok := e.failStart[id]; ok {
			e.writeJSON(w, http.StatusInternalServerError, map[string]string{"message": msg})
			return
		}
		if target.State == "running" {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		target.State = "running"
		w.WriteHeader(http.StatusNoContent)
	case "stop":
		if r.URL.Query().Get("t") != "10" {
			e.writeJSON(w, http.StatusBadRequest, map[string]string{"message": "t=10 required"})
			return
		}
		if target.State != "running" {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		target.State = "exited"
		w.WriteHeader(http.StatusNoContent)
	default:
		e.writeJSON(w, http.StatusNotFound, map[string]string{"message": "unknown action"})
	}
}

func matchFilters(c *fakeContainer, filters map[string][]string) bool {
	if names, ok := filters["name"]; ok {
		matched := false
		for _, pattern := range names {
			if regexp.MustCompile(pattern).MatchString("/" + c.Name) {
				matched = true
			}
		}
		if !matched {
			return false
		}
	}
	for _, label := range filters["label"] {
		key, value, hasValue := strings.Cut(label, "=")
		got, present := c.Labels[key]
		if !present || (hasValue && got != value) {
			return false
		}
	}
	return true
}

func TestDockerClientVersionNegotiation(t *testing.T) {
	engine := newFakeEngine(t, &fakeContainer{ID: "a1", Name: "web", State: "running"})
	engine.version = "1.41"
	client := engine.client()

	containers, err := client.listContainers(context.Background(), map[string][]string{"name": {"^/web$"}})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(containers) != 1 || containers[0].ID != "a1" || !containers[0].running() {
		t.Fatalf("containers = %+v", containers)
	}
	if _, err := client.listContainers(context.Background(), map[string][]string{}); err != nil {
		t.Fatalf("second list: %v", err)
	}
	versionCalls := 0
	for _, line := range engine.requestLog() {
		if line == "GET /version" {
			versionCalls++
		}
	}
	if versionCalls != 1 {
		t.Errorf("version calls = %d, want 1", versionCalls)
	}
	if got := engine.requestLog()[1]; got != "GET /v1.41/containers/json" {
		t.Errorf("second request = %q", got)
	}
}

func TestDockerClientVersionFailureIsFatal(t *testing.T) {
	tests := []struct {
		name    string
		version string
		wantErr string
	}{
		{"engine error", "", "version unavailable"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			engine := newFakeEngine(t)
			engine.version = tt.version
			_, err := engine.client().listContainers(context.Background(), nil)
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) || !strings.Contains(err.Error(), "negotiate docker api version") {
				t.Fatalf("err = %v", err)
			}
			for _, line := range engine.requestLog() {
				if strings.Contains(line, "containers") {
					t.Errorf("unexpected request after failed negotiation: %s", line)
				}
			}
		})
	}
}

func TestDockerClientUnreachable(t *testing.T) {
	engine := newFakeEngine(t)
	client := engine.client()
	engine.server.Close()
	_, err := client.listContainers(context.Background(), nil)
	if err == nil || !strings.Contains(err.Error(), "negotiate docker api version") {
		t.Fatalf("err = %v", err)
	}
}

func TestDockerClientHostParsing(t *testing.T) {
	tests := []struct {
		host    string
		wantErr bool
	}{
		{"unix:///var/run/docker.sock", false},
		{"tcp://127.0.0.1:2375", false},
		{"http://127.0.0.1:2375", false},
		{"unix://", true},
		{"tcp://", true},
		{"ftp://example.com", true},
		{"://bad", true},
	}
	for _, tt := range tests {
		t.Run(tt.host, func(t *testing.T) {
			_, err := newDockerClient(tt.host)
			if (err != nil) != tt.wantErr {
				t.Errorf("err = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestDockerClientErrorMessageIncluded(t *testing.T) {
	engine := newFakeEngine(t, &fakeContainer{ID: "a1", Name: "web", State: "exited"})
	engine.failStart["a1"] = "port is already allocated"
	err := engine.client().startContainer(context.Background(), "a1")
	if err == nil || !strings.Contains(err.Error(), "port is already allocated") {
		t.Fatalf("err = %v", err)
	}
	err = engine.client().startContainer(context.Background(), "missing")
	if err == nil || !strings.Contains(err.Error(), "No such container") {
		t.Fatalf("err = %v", err)
	}
}

func TestDockerClientNotModifiedAccepted(t *testing.T) {
	engine := newFakeEngine(t,
		&fakeContainer{ID: "r", Name: "run", State: "running"},
		&fakeContainer{ID: "s", Name: "stop", State: "exited"},
	)
	client := engine.client()
	if err := client.startContainer(context.Background(), "r"); err != nil {
		t.Errorf("start running: %v", err)
	}
	if err := client.stopContainer(context.Background(), "s"); err != nil {
		t.Errorf("stop stopped: %v", err)
	}
}

func TestDockerDriverState(t *testing.T) {
	tests := []struct {
		name       string
		containers []*fakeContainer
		want       State
	}{
		{"all running", []*fakeContainer{{ID: "1", Name: "a", State: "running"}, {ID: "2", Name: "b", State: "running"}}, Running},
		{"none running", []*fakeContainer{{ID: "1", Name: "a", State: "exited"}, {ID: "2", Name: "b", State: "created"}}, Stopped},
		{"mixed", []*fakeContainer{{ID: "1", Name: "a", State: "running"}, {ID: "2", Name: "b", State: "exited"}}, Partial},
		{"one missing", []*fakeContainer{{ID: "1", Name: "a", State: "running"}}, Partial},
		{"all missing", nil, Stopped},
		{"name is exact", []*fakeContainer{{ID: "1", Name: "a-extra", State: "running"}, {ID: "2", Name: "b", State: "running"}}, Partial},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			engine := newFakeEngine(t, tt.containers...)
			driver := newDockerDriver(engine.client(), DockerSpec{Containers: []string{"a", "b"}})
			got, err := driver.State(context.Background())
			if err != nil {
				t.Fatalf("State: %v", err)
			}
			if got != tt.want {
				t.Errorf("state = %s, want %s", got, tt.want)
			}
		})
	}
}

func TestDockerDriverStartStop(t *testing.T) {
	engine := newFakeEngine(t,
		&fakeContainer{ID: "1", Name: "a", State: "exited"},
		&fakeContainer{ID: "2", Name: "b", State: "running"},
		&fakeContainer{ID: "3", Name: "other", State: "exited"},
	)
	driver := newDockerDriver(engine.client(), DockerSpec{Containers: []string{"a", "b"}})
	ctx := context.Background()

	if err := driver.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if engine.stateOf("1") != "running" || engine.stateOf("3") != "exited" {
		t.Errorf("states after start: a=%s other=%s", engine.stateOf("1"), engine.stateOf("3"))
	}
	for _, line := range engine.requestLog() {
		if strings.HasSuffix(line, "/containers/2/start") {
			t.Errorf("already-running container was started: %s", line)
		}
	}
	if got, _ := driver.State(ctx); got != Running {
		t.Errorf("state = %s", got)
	}
	if err := driver.Stop(ctx); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if engine.stateOf("1") != "exited" || engine.stateOf("2") != "exited" || engine.stateOf("3") != "exited" {
		t.Errorf("states after stop: %s %s %s", engine.stateOf("1"), engine.stateOf("2"), engine.stateOf("3"))
	}
	if got, _ := driver.State(ctx); got != Stopped {
		t.Errorf("state = %s", got)
	}
}

func TestDockerDriverStartMissingContainer(t *testing.T) {
	engine := newFakeEngine(t, &fakeContainer{ID: "1", Name: "a", State: "exited"})
	driver := newDockerDriver(engine.client(), DockerSpec{Containers: []string{"a", "b"}})
	err := driver.Start(context.Background())
	if err == nil || !strings.Contains(err.Error(), "found 1 of 2") {
		t.Fatalf("err = %v", err)
	}
}

func TestDockerDriverStartFailureReportsEngineMessage(t *testing.T) {
	engine := newFakeEngine(t, &fakeContainer{ID: "1", Name: "a", State: "exited"})
	engine.failStart["1"] = "oci runtime error"
	driver := newDockerDriver(engine.client(), DockerSpec{Containers: []string{"a"}})
	err := driver.Start(context.Background())
	if err == nil || !strings.Contains(err.Error(), "oci runtime error") || !strings.Contains(err.Error(), "container a") {
		t.Fatalf("err = %v", err)
	}
}

func TestDockerDriverListFailure(t *testing.T) {
	engine := newFakeEngine(t)
	engine.failList = true
	driver := newDockerDriver(engine.client(), DockerSpec{Label: "x=y"})
	if _, err := driver.State(context.Background()); err == nil || !strings.Contains(err.Error(), "list exploded") {
		t.Fatalf("err = %v", err)
	}
}

func TestDockerDriverLabel(t *testing.T) {
	engine := newFakeEngine(t,
		&fakeContainer{ID: "1", Name: "cockpit", State: "exited", Labels: map[string]string{"mishmesh.service": "cockpit"}},
		&fakeContainer{ID: "2", Name: "other", State: "exited", Labels: map[string]string{"mishmesh.service": "other"}},
	)
	driver := newDockerDriver(engine.client(), DockerSpec{Label: "mishmesh.service=cockpit"})
	if err := driver.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if engine.stateOf("1") != "running" || engine.stateOf("2") != "exited" {
		t.Errorf("states: %s %s", engine.stateOf("1"), engine.stateOf("2"))
	}
}

func TestDockerDriverLabelNoMatchFailsStart(t *testing.T) {
	engine := newFakeEngine(t)
	driver := newDockerDriver(engine.client(), DockerSpec{Label: "mishmesh.service=none"})
	if err := driver.Start(context.Background()); err == nil {
		t.Fatal("expected error")
	}
}

func composeLabels(project, service string) map[string]string {
	return map[string]string{"com.docker.compose.project": project, "com.docker.compose.service": service}
}

func TestComposeDriverByLabelAndServices(t *testing.T) {
	engine := newFakeEngine(t,
		&fakeContainer{ID: "1", Name: "affine-web-1", State: "exited", Labels: composeLabels("affine", "web")},
		&fakeContainer{ID: "2", Name: "affine-db-1", State: "exited", Labels: composeLabels("affine", "db")},
		&fakeContainer{ID: "3", Name: "affine-redis-1", State: "exited", Labels: composeLabels("affine", "redis")},
		&fakeContainer{ID: "4", Name: "other-web-1", State: "exited", Labels: composeLabels("other", "web")},
	)
	ctx := context.Background()

	all := newComposeDriver(engine.client(), ComposeSpec{Project: "affine", Bin: "docker"})
	if err := all.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	for _, id := range []string{"1", "2", "3"} {
		if engine.stateOf(id) != "running" {
			t.Errorf("container %s = %s", id, engine.stateOf(id))
		}
	}
	if engine.stateOf("4") != "exited" {
		t.Errorf("other project container touched")
	}
	if got, _ := all.State(ctx); got != Running {
		t.Errorf("state = %s", got)
	}
	if err := all.Stop(ctx); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if got, _ := all.State(ctx); got != Stopped {
		t.Errorf("state = %s", got)
	}

	filtered := newComposeDriver(engine.client(), ComposeSpec{Project: "affine", Bin: "docker", Services: []string{"web", "db"}})
	if err := filtered.Start(ctx); err != nil {
		t.Fatalf("filtered Start: %v", err)
	}
	if engine.stateOf("1") != "running" || engine.stateOf("2") != "running" || engine.stateOf("3") != "exited" {
		t.Errorf("filtered states: %s %s %s", engine.stateOf("1"), engine.stateOf("2"), engine.stateOf("3"))
	}
	if got, _ := filtered.State(ctx); got != Running {
		t.Errorf("filtered state = %s", got)
	}
}

func TestComposeDriverUpWhenNoContainers(t *testing.T) {
	tests := []struct {
		name     string
		spec     ComposeSpec
		runErr   error
		wantArgv []string
		wantErr  string
	}{
		{
			name:     "file configured",
			spec:     ComposeSpec{Project: "affine", File: "/opt/homelab/stacks/affine/compose.yaml", Bin: "docker"},
			wantArgv: []string{"docker", "compose", "-f", "/opt/homelab/stacks/affine/compose.yaml", "-p", "affine", "up", "-d"},
		},
		{
			name:     "custom bin and services",
			spec:     ComposeSpec{Project: "p", File: "c.yaml", Bin: "/usr/bin/podman", Services: []string{"web"}},
			wantArgv: []string{"/usr/bin/podman", "compose", "-f", "c.yaml", "-p", "p", "up", "-d", "web"},
		},
		{
			name:    "no file",
			spec:    ComposeSpec{Project: "affine", Bin: "docker"},
			wantErr: "no file configured",
		},
		{
			name:     "up fails",
			spec:     ComposeSpec{Project: "affine", File: "c.yaml", Bin: "docker"},
			runErr:   context.DeadlineExceeded,
			wantArgv: []string{"docker", "compose", "-f", "c.yaml", "-p", "affine", "up", "-d"},
			wantErr:  "compose up",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			engine := newFakeEngine(t)
			driver := newComposeDriver(engine.client(), tt.spec)
			var gotArgv []string
			driver.run = func(_ context.Context, argv []string, _ string, _ map[string]string) (string, error) {
				gotArgv = argv
				return "", tt.runErr
			}
			err := driver.Start(context.Background())
			if tt.wantErr == "" && err != nil {
				t.Fatalf("Start: %v", err)
			}
			if tt.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tt.wantErr)) {
				t.Fatalf("err = %v, want %q", err, tt.wantErr)
			}
			if !reflect.DeepEqual(gotArgv, tt.wantArgv) {
				t.Errorf("argv = %v, want %v", gotArgv, tt.wantArgv)
			}
		})
	}
}

func TestComposeDriverSkipsUpWhenContainersExist(t *testing.T) {
	engine := newFakeEngine(t, &fakeContainer{ID: "1", Name: "x", State: "exited", Labels: composeLabels("affine", "web")})
	driver := newComposeDriver(engine.client(), ComposeSpec{Project: "affine", File: "c.yaml", Bin: "docker"})
	driver.run = func(context.Context, []string, string, map[string]string) (string, error) {
		t.Error("compose up must not run when containers exist")
		return "", nil
	}
	if err := driver.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if engine.stateOf("1") != "running" {
		t.Errorf("state = %s", engine.stateOf("1"))
	}
}

func TestDockerClientUnixSocketHostRejectsMissingSocket(t *testing.T) {
	client, err := newDockerClient("unix:///nonexistent/mishmesh-test.sock")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := client.listContainers(ctx, nil); err == nil {
		t.Fatal("expected dial error")
	}
}
