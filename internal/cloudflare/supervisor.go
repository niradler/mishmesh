package cloudflare

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

const (
	supervisorMinBackoff   = time.Second
	supervisorMaxBackoff   = 60 * time.Second
	supervisorHealthyAfter = 60 * time.Second
	supervisorWaitDelay    = 5 * time.Second
	readyTimeout           = 2 * time.Second
)

type SupervisorStatus struct {
	Running  bool   `json:"running"`
	PID      int    `json:"pid,omitempty"`
	Restarts int    `json:"restarts"`
	LastExit string `json:"last_exit,omitempty"`
	Ready    bool   `json:"ready"`
}

type Supervisor struct {
	cfg          SupervisorConfig
	log          *slog.Logger
	minBackoff   time.Duration
	maxBackoff   time.Duration
	healthyAfter time.Duration

	mu       sync.Mutex
	running  bool
	pid      int
	restarts int
	lastExit string
}

func NewSupervisor(cfg SupervisorConfig, log *slog.Logger) *Supervisor {
	if cfg.Bin == "" {
		cfg.Bin = DefaultCloudflared
	}
	if cfg.MetricsAddr == "" {
		cfg.MetricsAddr = DefaultMetricsAddr
	}
	if log == nil {
		log = slog.Default()
	}
	return &Supervisor{
		cfg:          cfg,
		log:          log,
		minBackoff:   supervisorMinBackoff,
		maxBackoff:   supervisorMaxBackoff,
		healthyAfter: supervisorHealthyAfter,
	}
}

func (s *Supervisor) Run(ctx context.Context) {
	backoff := s.minBackoff
	for first := true; ctx.Err() == nil; first = false {
		if !first {
			s.mu.Lock()
			s.restarts++
			s.mu.Unlock()
		}
		started := time.Now()
		err := s.runOnce(ctx)
		if ctx.Err() != nil {
			return
		}
		s.mu.Lock()
		s.lastExit = exitText(err)
		s.mu.Unlock()
		if time.Since(started) >= s.healthyAfter {
			backoff = s.minBackoff
		}
		s.log.Warn("cloudflared exited", "error", exitText(err), "restart_in", backoff)
		if sleepCtx(ctx, backoff) != nil {
			return
		}
		backoff = min(backoff*2, s.maxBackoff)
	}
}

func exitText(err error) string {
	if err == nil {
		return "exited"
	}
	return err.Error()
}

func (s *Supervisor) runOnce(ctx context.Context) error {
	raw, err := os.ReadFile(s.cfg.TokenFile)
	if err != nil {
		return fmt.Errorf("read tunnel token file: %w", err)
	}
	token := strings.TrimSpace(string(raw))
	if token == "" {
		return errors.New("tunnel token file is empty")
	}
	cmd := exec.CommandContext(ctx, s.cfg.Bin, "tunnel", "--no-autoupdate", "--metrics", s.cfg.MetricsAddr, "run")
	cmd.Env = append(os.Environ(), "TUNNEL_TOKEN="+token)
	cmd.WaitDelay = supervisorWaitDelay
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start %s: %w", s.cfg.Bin, err)
	}
	s.mu.Lock()
	s.running = true
	s.pid = cmd.Process.Pid
	s.mu.Unlock()
	werr := cmd.Wait()
	s.mu.Lock()
	s.running = false
	s.pid = 0
	s.mu.Unlock()
	return werr
}

func (s *Supervisor) Status() SupervisorStatus {
	s.mu.Lock()
	st := SupervisorStatus{Running: s.running, PID: s.pid, Restarts: s.restarts, LastExit: s.lastExit}
	s.mu.Unlock()
	if st.Running {
		st.Ready = s.ready()
	}
	return st
}

func (s *Supervisor) ready() bool {
	ctx, cancel := context.WithTimeout(context.Background(), readyTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+s.cfg.MetricsAddr+"/ready", nil)
	if err != nil {
		return false
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}
