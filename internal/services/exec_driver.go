package services

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"time"
)

const (
	maxCommandOutput = 2048
	commandWaitDelay = 2 * time.Second
)

type cappedBuffer struct {
	buf bytes.Buffer
}

func (b *cappedBuffer) Write(p []byte) (int, error) {
	if remaining := maxCommandOutput - b.buf.Len(); remaining > 0 {
		b.buf.Write(p[:min(len(p), remaining)])
	}
	return len(p), nil
}

func runCommand(ctx context.Context, argv []string, dir string, env map[string]string) (string, error) {
	if len(argv) == 0 {
		return "", errors.New("empty command")
	}
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir = dir
	if len(env) > 0 {
		merged := os.Environ()
		for key, value := range env {
			merged = append(merged, key+"="+value)
		}
		cmd.Env = merged
	}
	cmd.WaitDelay = commandWaitDelay
	var out cappedBuffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	err := cmd.Run()
	output := out.buf.String()
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			err = fmt.Errorf("%w: %w", ctxErr, err)
		}
		return output, fmt.Errorf("run %s: %w (output: %q)", argv[0], err, output)
	}
	return output, nil
}

type execDriver struct {
	spec ExecSpec
	run  commandRunner
}

func newExecDriver(spec ExecSpec) *execDriver {
	return &execDriver{spec: spec, run: runCommand}
}

func (d *execDriver) Start(ctx context.Context) error {
	_, err := d.run(ctx, d.spec.Start, d.spec.Dir, d.spec.Env)
	return err
}

func (d *execDriver) Stop(ctx context.Context) error {
	_, err := d.run(ctx, d.spec.Stop, d.spec.Dir, d.spec.Env)
	return err
}

func (d *execDriver) State(ctx context.Context) (State, error) {
	if len(d.spec.Status) == 0 {
		return Unknown, nil
	}
	_, err := d.run(ctx, d.spec.Status, d.spec.Dir, d.spec.Env)
	if err == nil {
		return Running, nil
	}
	if ctx.Err() != nil {
		return Unknown, err
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return Stopped, nil
	}
	return Unknown, err
}
