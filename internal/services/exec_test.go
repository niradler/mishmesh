package services

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

const helperEnvKey = "MISHMESH_SERVICES_HELPER"

func TestHelperProcess(t *testing.T) {
	mode := os.Getenv(helperEnvKey)
	if mode == "" {
		return
	}
	switch mode {
	case "ok":
		fmt.Print("hello")
		os.Exit(0)
	case "fail":
		fmt.Fprint(os.Stderr, "boom")
		os.Exit(3)
	case "fail-secrets":
		fmt.Fprint(os.Stderr, os.Getenv("SERVICES_CONFIGURED_SECRET")+"|"+os.Getenv("SERVICES_INHERITED_SECRET"))
		os.Exit(3)
	case "flood":
		fmt.Print(strings.Repeat("x", 10000))
		os.Exit(4)
	case "env":
		fmt.Print(os.Getenv("SERVICES_TEST_VALUE") + "|" + os.Getenv("SERVICES_TEST_KEEP"))
		os.Exit(0)
	case "pwd":
		dir, _ := os.Getwd()
		fmt.Print(dir)
		os.Exit(0)
	case "sleep":
		time.Sleep(30 * time.Second)
		os.Exit(0)
	}
	os.Exit(9)
}

func helperArgv() []string {
	return []string{os.Args[0], "-test.run=^TestHelperProcess$"}
}

func helperEnv(mode string, extra map[string]string) map[string]string {
	env := map[string]string{helperEnvKey: mode}
	for k, v := range extra {
		env[k] = v
	}
	return env
}

func TestRunCommandOutcomes(t *testing.T) {
	tests := []struct {
		name       string
		mode       string
		wantErr    bool
		wantOutput string
		wantInErr  string
	}{
		{"success", "ok", false, "hello", ""},
		{"failure returns output internally", "fail", true, "boom", "exit status"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, err := runCommand(context.Background(), helperArgv(), "", helperEnv(tt.mode, nil))
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v", err)
			}
			if !strings.Contains(out, tt.wantOutput) {
				t.Errorf("output = %q", out)
			}
			if tt.wantInErr != "" && !strings.Contains(err.Error(), tt.wantInErr) {
				t.Errorf("err %q missing %q", err, tt.wantInErr)
			}
			if err != nil && strings.Contains(err.Error(), tt.wantOutput) {
				t.Fatal("captured output leaked into error")
			}
		})
	}
}

func TestRunCommandTruncatesOutput(t *testing.T) {
	out, err := runCommand(context.Background(), helperArgv(), "", helperEnv("flood", nil))
	if err == nil {
		t.Fatal("expected error")
	}
	if len(out) != maxCommandOutput {
		t.Errorf("output len = %d, want %d", len(out), maxCommandOutput)
	}
	if len(err.Error()) > maxCommandOutput+512 {
		t.Errorf("error len = %d, not truncated", len(err.Error()))
	}
}

func TestRunCommandMergesEnv(t *testing.T) {
	t.Setenv("SERVICES_TEST_KEEP", "kept")
	t.Setenv("SERVICES_TEST_VALUE", "original")
	out, err := runCommand(context.Background(), helperArgv(), "", helperEnv("env", map[string]string{"SERVICES_TEST_VALUE": "override"}))
	if err != nil {
		t.Fatal(err)
	}
	if out != "override|kept" {
		t.Errorf("output = %q", out)
	}
}

func TestRunCommandDir(t *testing.T) {
	dir := t.TempDir()
	out, err := runCommand(context.Background(), helperArgv(), dir, helperEnv("pwd", nil))
	if err != nil {
		t.Fatal(err)
	}
	want, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.Stat(out)
	if err != nil {
		t.Fatalf("stat %q: %v", out, err)
	}
	if !os.SameFile(want, got) {
		t.Errorf("working dir = %q, want %q", out, dir)
	}
}

func TestRunCommandContextTimeout(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	begin := time.Now()
	_, err := runCommand(ctx, helperArgv(), "", helperEnv("sleep", nil))
	if err == nil {
		t.Fatal("expected error")
	}
	if elapsed := time.Since(begin); elapsed > 10*time.Second {
		t.Errorf("took %v, context not respected", elapsed)
	}
	if ctx.Err() == nil {
		t.Error("context should be expired")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("context cause lost: %v", err)
	}
}

func TestRunCommandErrors(t *testing.T) {
	if _, err := runCommand(context.Background(), nil, "", nil); err == nil {
		t.Error("expected error for empty argv")
	}
	if _, err := runCommand(context.Background(), []string{"mishmesh-nonexistent-binary"}, "", nil); err == nil {
		t.Error("expected error for missing binary")
	}
}

func TestExecDriverState(t *testing.T) {
	tests := []struct {
		name    string
		status  []string
		mode    string
		want    State
		wantErr bool
	}{
		{"status exit 0", helperArgv(), "ok", Running, false},
		{"status non-zero", helperArgv(), "fail", Stopped, false},
		{"status absent", nil, "ok", Unknown, false},
		{"status binary missing", []string{"mishmesh-nonexistent-binary"}, "ok", Unknown, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			driver := newExecDriver(ExecSpec{
				Start:  helperArgv(),
				Stop:   helperArgv(),
				Status: tt.status,
				Env:    helperEnv(tt.mode, nil),
			})
			got, err := driver.State(context.Background())
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v", err)
			}
			if got != tt.want {
				t.Errorf("state = %s, want %s", got, tt.want)
			}
		})
	}
}

func TestExecDriverStatusTimeoutIsError(t *testing.T) {
	driver := newExecDriver(ExecSpec{
		Start:  helperArgv(),
		Stop:   helperArgv(),
		Status: helperArgv(),
		Env:    helperEnv("sleep", nil),
	})
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	got, err := driver.State(ctx)
	if err == nil || got != Unknown {
		t.Fatalf("state = %s, err = %v", got, err)
	}
}

func TestExecDriverStartStop(t *testing.T) {
	good := newExecDriver(ExecSpec{Start: helperArgv(), Stop: helperArgv(), Env: helperEnv("ok", nil)})
	if err := good.Start(context.Background()); err != nil {
		t.Errorf("Start: %v", err)
	}
	if err := good.Stop(context.Background()); err != nil {
		t.Errorf("Stop: %v", err)
	}
	bad := newExecDriver(ExecSpec{Start: helperArgv(), Stop: helperArgv(), Env: helperEnv("fail", nil)})
	if err := bad.Start(context.Background()); err == nil || !strings.Contains(err.Error(), "exit status") || strings.Contains(err.Error(), "boom") {
		t.Errorf("Start err = %v", err)
	}
	if err := bad.Stop(context.Background()); err == nil || !strings.Contains(err.Error(), "exit status") || strings.Contains(err.Error(), "boom") {
		t.Errorf("Stop err = %v", err)
	}
}

func TestShouldExcludeCommandOutputFromErrorsStatusAndLogs(t *testing.T) {
	configured, inherited := "configured-test-secret", "inherited-test-secret"
	t.Setenv("SERVICES_INHERITED_SECRET", inherited)
	env := helperEnv("fail-secrets", map[string]string{"SERVICES_CONFIGURED_SECRET": configured})
	output, err := runCommand(context.Background(), helperArgv(), "", env)
	if err == nil || output != configured+"|"+inherited {
		t.Fatalf("missing internal output or exit error: %v", err)
	}
	var exitError *exec.ExitError
	if !errors.As(err, &exitError) || exitError.ExitCode() != 3 {
		t.Fatalf("exit cause lost: %v", err)
	}
	for _, secret := range []string{configured, inherited} {
		if strings.Contains(err.Error(), secret) {
			t.Fatal("output exposed by runner error")
		}
	}
	config, err := json.Marshal(ExecSpec{Start: helperArgv(), Stop: helperArgv(), Env: env})
	if err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	manager := NewManager(Options{Registry: testRegistry(t), HostCommands: true, DockerHost: "ftp://unused", Log: slog.New(slog.NewTextHandler(&logs, nil))})
	manager.Apply([]Definition{{ID: "svc_secret", Name: "secret", Driver: DriverCommand, Config: config, StartTimeout: 5 * time.Second}})
	err = manager.EnsureRunning(context.Background(), "svc_secret", "")
	if err == nil {
		t.Fatal("expected start failure")
	}
	status, ok := manager.Status("svc_secret")
	if !ok || status.State != StateError {
		t.Fatalf("status = %+v", status)
	}
	for _, secret := range []string{configured, inherited} {
		if strings.Contains(err.Error(), secret) || strings.Contains(status.LastError, secret) || strings.Contains(logs.String(), secret) {
			t.Fatal("command output exposed by manager")
		}
	}
}
