package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"math/rand/v2"
	"net"
	"net/http"
	"os"
	"os/signal"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/mishmesh/mishmesh/internal/tunnel"
)

const (
	initialBackoff     = time.Second
	maxBackoff         = 30 * time.Second
	stableSessionAfter = 10 * time.Second
	pingEvery          = 30 * time.Second
)

type agentStats struct {
	connected  atomic.Int64
	attempts   atomic.Int64
	fails      atomic.Int64
	reconnects atomic.Int64
	streams    atomic.Int64
	mu         sync.Mutex
	lat        []time.Duration
	errs       map[string]int
}

func (s *agentStats) recordLatency(d time.Duration) {
	s.mu.Lock()
	s.lat = append(s.lat, d)
	s.mu.Unlock()
}

func (s *agentStats) recordErr(err error) {
	msg := err.Error()
	if len(msg) > 90 {
		msg = msg[:90]
	}
	s.mu.Lock()
	s.errs[msg]++
	s.mu.Unlock()
}

func (s *agentStats) snapshotLat(from int) []time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]time.Duration, len(s.lat)-from)
	copy(out, s.lat[from:])
	return out
}

func (s *agentStats) latLen() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.lat)
}

type agentConfig struct {
	gateway string
	backend string
	subBase string
}

func percentile(sorted []time.Duration, p float64) time.Duration {
	if len(sorted) == 0 {
		return 0
	}
	idx := int(float64(len(sorted)-1) * p)
	return sorted[idx]
}

func sortedCopy(in []time.Duration) []time.Duration {
	out := append([]time.Duration(nil), in...)
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

func mintTokens(api, prefix string, n, conc int, cacheFile string) ([]string, error) {
	if data, err := os.ReadFile(cacheFile); err == nil {
		lines := strings.Fields(string(data))
		if len(lines) >= n {
			return lines[:n], nil
		}
	}
	client := &http.Client{Timeout: 30 * time.Second}
	tokens := make([]string, n)
	var wg sync.WaitGroup
	sem := make(chan struct{}, conc)
	var firstErr atomic.Value
	for i := 0; i < n; i++ {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int) {
			defer wg.Done()
			defer func() { <-sem }()
			body, _ := json.Marshal(map[string]string{"name": fmt.Sprintf("%s%d", prefix, i)})
			resp, err := client.Post(api+"/api/v1/agents", "application/json", bytes.NewReader(body))
			if err != nil {
				firstErr.CompareAndSwap(nil, err)
				return
			}
			defer resp.Body.Close()
			raw, _ := io.ReadAll(resp.Body)
			var out struct {
				Token string `json:"token"`
			}
			if resp.StatusCode != http.StatusCreated || json.Unmarshal(raw, &out) != nil || out.Token == "" {
				firstErr.CompareAndSwap(nil, fmt.Errorf("mint %d: %d %s", i, resp.StatusCode, string(raw)))
				return
			}
			tokens[i] = out.Token
		}(i)
	}
	wg.Wait()
	if e := firstErr.Load(); e != nil {
		return nil, e.(error)
	}
	if err := os.WriteFile(cacheFile, []byte(strings.Join(tokens, "\n")), 0o600); err != nil {
		return nil, err
	}
	return tokens, nil
}

func jittered(d time.Duration) time.Duration {
	half := d / 2
	return half + time.Duration(rand.Int64N(int64(half)+1))
}

func runAgent(ctx context.Context, cfg agentConfig, idx int, token string, st *agentStats) {
	backoff := initialBackoff
	first := true
	for {
		started := time.Now()
		if !first {
			st.reconnects.Add(1)
		}
		first = false
		err := connectOnce(ctx, cfg, idx, token, st)
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			st.recordErr(err)
		}
		if time.Since(started) >= stableSessionAfter {
			backoff = initialBackoff
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(jittered(backoff)):
		}
		if backoff *= 2; backoff > maxBackoff {
			backoff = maxBackoff
		}
	}
}

func connectOnce(parent context.Context, cfg agentConfig, idx int, token string, st *agentStats) error {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	st.attempts.Add(1)
	t0 := time.Now()
	fail := func(err error) error {
		st.fails.Add(1)
		return err
	}

	conn, err := tunnel.Dial(ctx, strings.TrimRight(cfg.gateway, "/")+tunnel.AgentConnectPath, tunnel.DialOptions{Token: token})
	if err != nil {
		return fail(fmt.Errorf("dial: %w", err))
	}
	sess, err := tunnel.Client(conn)
	if err != nil {
		_ = conn.Close()
		return fail(err)
	}
	defer sess.Close()
	ctrl, err := sess.OpenControl()
	if err != nil {
		return fail(fmt.Errorf("open control: %w", err))
	}
	reg := tunnel.RegisterPayload{Endpoints: []tunnel.EndpointRequest{{
		Ref: "0", Kind: "http", Lifecycle: "ephemeral", Subdomain: fmt.Sprintf("%s%d", cfg.subBase, idx),
	}}}
	if err := ctrl.Send(tunnel.ControlMessage{Type: tunnel.MsgRegister, Register: &reg}); err != nil {
		return fail(fmt.Errorf("send register: %w", err))
	}

	acked := make(chan bool, 1)
	go func() {
		signalled := false
		for ctx.Err() == nil {
			msg, err := ctrl.Recv()
			if err != nil {
				if !signalled {
					acked <- false
				}
				return
			}
			if msg.Type == tunnel.MsgRegisterAck && !signalled {
				signalled = true
				ok := msg.RegisterAck != nil && len(msg.RegisterAck.Endpoints) > 0 && msg.RegisterAck.Endpoints[0].EndpointID != ""
				acked <- ok
			}
		}
	}()
	select {
	case ok := <-acked:
		if !ok {
			return fail(fmt.Errorf("register not acked"))
		}
	case <-time.After(60 * time.Second):
		return fail(fmt.Errorf("register ack timeout"))
	case <-ctx.Done():
		return nil
	}
	st.recordLatency(time.Since(t0))
	st.connected.Add(1)
	defer st.connected.Add(-1)

	go func() {
		t := time.NewTicker(pingEvery)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				if ctrl.Send(tunnel.ControlMessage{Type: tunnel.MsgPing}) != nil {
					return
				}
			}
		}
	}()

	for {
		stream, _, err := sess.AcceptData()
		if err != nil {
			return fmt.Errorf("session ended: %w", err)
		}
		st.streams.Add(1)
		go proxyStream(stream, cfg.backend)
	}
}

func proxyStream(stream net.Conn, backend string) {
	defer stream.Close()
	local, err := net.Dial("tcp", backend)
	if err != nil {
		return
	}
	defer local.Close()
	done := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(local, stream); done <- struct{}{} }()
	go func() { _, _ = io.Copy(stream, local); done <- struct{}{} }()
	<-done
}

func parseSteps(s string) ([]int, error) {
	var out []int
	for _, p := range strings.Split(s, ",") {
		n, err := strconv.Atoi(strings.TrimSpace(p))
		if err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, nil
}

func runAgents(args []string) error {
	fs := flag.NewFlagSet("agents", flag.ExitOnError)
	api := fs.String("api", "http://server-a:8081", "control API base URL (token minting)")
	gateway := fs.String("gateway", "ws://server-a:8081", "gateway websocket URL")
	backend := fs.String("backend", "127.0.0.1:9000", "local backend each agent forwards to")
	prefix := fs.String("prefix", "p", "subdomain and agent-name prefix")
	steps := fs.String("steps", "100", "comma separated cumulative agent counts")
	rate := fs.Int("rate", 250, "agent start rate per second (0 = all at once)")
	hold := fs.Duration("hold", 30*time.Second, "pause at each step for sampling")
	stepTimeout := fs.Duration("step-timeout", 120*time.Second, "max wait for a step to connect")
	finalHold := fs.Duration("final-hold", 0, "stay connected after last step (0 = until SIGTERM)")
	tokensDir := fs.String("tokens-dir", "/out", "token cache directory")
	timeline := fs.String("timeline", "", "CSV file for per-second connected counts")
	mintConc := fs.Int("mint-conc", 32, "token mint concurrency")
	_ = fs.Parse(args)

	stepList, err := parseSteps(*steps)
	if err != nil {
		return err
	}
	maxN := stepList[len(stepList)-1]
	t0 := time.Now()
	tokens, err := mintTokens(*api, *prefix, maxN, *mintConc, fmt.Sprintf("%s/tokens-%s-%d.txt", *tokensDir, *prefix, maxN))
	if err != nil {
		return err
	}
	fmt.Printf("minted %d tokens in %s\n", len(tokens), time.Since(t0).Round(time.Millisecond))

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	st := &agentStats{errs: map[string]int{}}
	cfg := agentConfig{gateway: *gateway, backend: *backend, subBase: *prefix}

	var tl *bufio.Writer
	if *timeline != "" {
		f, err := os.Create(*timeline)
		if err != nil {
			return err
		}
		defer f.Close()
		tl = bufio.NewWriter(f)
		fmt.Fprintln(tl, "ts_ms,connected,attempts,fails,reconnects")
		go func() {
			t := time.NewTicker(500 * time.Millisecond)
			defer t.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case now := <-t.C:
					fmt.Fprintf(tl, "%d,%d,%d,%d,%d\n", now.UnixMilli(), st.connected.Load(), st.attempts.Load(), st.fails.Load(), st.reconnects.Load())
					_ = tl.Flush()
				}
			}
		}()
	}

	launched := 0
	for si, target := range stepList {
		stepStart := time.Now()
		latFrom := st.latLen()
		failsFrom := st.fails.Load()
		var tick <-chan time.Time
		if *rate > 0 {
			t := time.NewTicker(time.Second / time.Duration(*rate))
			defer t.Stop()
			tick = t.C
		}
		for ; launched < target; launched++ {
			if tick != nil {
				select {
				case <-tick:
				case <-ctx.Done():
					return nil
				}
			}
			go runAgent(ctx, cfg, launched, tokens[launched], st)
		}
		deadline := time.After(*stepTimeout)
	wait:
		for st.connected.Load() < int64(target) {
			select {
			case <-time.After(100 * time.Millisecond):
			case <-deadline:
				break wait
			case <-ctx.Done():
				return nil
			}
		}
		rampDone := time.Now()
		lat := sortedCopy(st.snapshotLat(latFrom))
		fmt.Printf("STEP target=%d connected=%d ramp_s=%.1f connect_p50_ms=%.1f p95_ms=%.1f p99_ms=%.1f max_ms=%.1f failed_attempts=%d start=%d end=%d\n",
			target, st.connected.Load(), rampDone.Sub(stepStart).Seconds(),
			ms(percentile(lat, 0.5)), ms(percentile(lat, 0.95)), ms(percentile(lat, 0.99)), ms(percentile(lat, 1)),
			st.fails.Load()-failsFrom, stepStart.UnixMilli(), rampDone.UnixMilli())
		st.mu.Lock()
		for k, v := range st.errs {
			fmt.Printf("  ERR x%d %s\n", v, k)
		}
		st.mu.Unlock()
		if si < len(stepList)-1 {
			select {
			case <-time.After(*hold):
			case <-ctx.Done():
				return nil
			}
			fmt.Printf("HOLD_END step=%d ts=%d\n", target, time.Now().UnixMilli())
		}
	}
	fmt.Printf("READY connected=%d ts=%d\n", st.connected.Load(), time.Now().UnixMilli())
	if *finalHold > 0 {
		select {
		case <-time.After(*finalHold):
		case <-ctx.Done():
		}
	} else {
		<-ctx.Done()
	}
	fmt.Printf("FINAL connected=%d attempts=%d fails=%d reconnects=%d streams=%d\n",
		st.connected.Load(), st.attempts.Load(), st.fails.Load(), st.reconnects.Load(), st.streams.Load())
	return nil
}

func ms(d time.Duration) float64 { return float64(d.Microseconds()) / 1000 }
