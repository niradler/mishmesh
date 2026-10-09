package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
)

func httpClient(insecureTLS bool) *http.Client {
	tr := &http.Transport{
		TLSClientConfig:     &tls.Config{InsecureSkipVerify: insecureTLS},
		MaxIdleConnsPerHost: 1000,
		DisableCompression:  true,
	}
	return &http.Client{Transport: tr}
}

func withHost(req *http.Request, host string) {
	if host != "" {
		req.Host = host
	}
}

func cmdUpload(args []string) error {
	fs := flag.NewFlagSet("upload", flag.ContinueOnError)
	target := fs.String("url", "", "upload url")
	host := fs.String("host", "", "Host header")
	size := fs.String("size", "1G", "bytes")
	seed := fs.Uint64("seed", 7, "seed")
	chunked := fs.Bool("chunked", false, "send Transfer-Encoding: chunked")
	if err := fs.Parse(args); err != nil {
		return err
	}
	n, err := parseSize(*size)
	if err != nil {
		return err
	}
	req, err := http.NewRequest(http.MethodPost, *target, io.NopCloser(newGen(n, *seed)))
	if err != nil {
		return err
	}
	withHost(req, *host)
	if !*chunked {
		req.ContentLength = n
	} else {
		req.ContentLength = -1
	}
	start := time.Now()
	resp, err := httpClient(true).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	elapsed := time.Since(start)
	var got struct {
		Bytes int64    `json:"bytes"`
		Sha   string   `json:"sha256"`
		CL    int64    `json:"content_length"`
		TE    []string `json:"te"`
		Error string   `json:"error"`
	}
	if err := json.Unmarshal(body, &got); err != nil {
		return fmt.Errorf("status %d body %q", resp.StatusCode, truncate(body))
	}
	want := genSha(n, *seed)
	fmt.Printf("upload status=%d bytes=%d cl=%d te=%v elapsed=%s rate=%.1fMB/s\n", resp.StatusCode, got.Bytes, got.CL, got.TE, elapsed.Round(time.Millisecond), float64(n)/elapsed.Seconds()/1e6)
	if got.Sha != want || got.Bytes != n {
		return fmt.Errorf("sha mismatch got=%s want=%s err=%s", got.Sha, want, got.Error)
	}
	fmt.Println("upload sha256 ok", want)
	return nil
}

func truncate(b []byte) string {
	if len(b) > 300 {
		return string(b[:300]) + "..."
	}
	return string(b)
}

func cmdWS(args []string) error {
	fs := flag.NewFlagSet("ws", flag.ContinueOnError)
	target := fs.String("url", "", "ws:// or wss:// url")
	host := fs.String("host", "", "Host header")
	size := fs.String("size", "16M", "message size")
	count := fs.Int("n", 4, "messages")
	idle := fs.Duration("idle", 0, "idle period between first and second message")
	if err := fs.Parse(args); err != nil {
		return err
	}
	n, err := parseSize(*size)
	if err != nil {
		return err
	}
	hdr := http.Header{}
	if *host != "" {
		hdr.Set("Host", *host)
	}
	ctx := context.Background()
	c, _, err := websocket.Dial(ctx, *target, &websocket.DialOptions{HTTPHeader: hdr, HTTPClient: httpClient(true), Host: *host})
	if err != nil {
		return fmt.Errorf("dial: %w", err)
	}
	defer c.CloseNow()
	c.SetReadLimit(256 << 20)
	start := time.Now()
	for i := range *count {
		if i == 1 && *idle > 0 {
			fmt.Printf("ws idling %s\n", *idle)
			time.Sleep(*idle)
		}
		msg := make([]byte, n)
		_, _ = newGen(n, uint64(i+100)).Read(msg)
		t0 := time.Now()
		if err := c.Write(ctx, websocket.MessageBinary, msg); err != nil {
			return fmt.Errorf("write %d: %w", i, err)
		}
		_, got, err := c.Read(ctx)
		if err != nil {
			return fmt.Errorf("read %d: %w", i, err)
		}
		if !bytes.Equal(got, msg) {
			return fmt.Errorf("echo %d mismatch len=%d", i, len(got))
		}
		fmt.Printf("ws msg %d size=%d rtt=%s ok\n", i, n, time.Since(t0).Round(time.Millisecond))
	}
	_ = c.Close(websocket.StatusNormalClosure, "")
	fmt.Printf("ws ok total=%s\n", time.Since(start).Round(time.Millisecond))
	return nil
}

func cmdSSE(args []string) error {
	fs := flag.NewFlagSet("sse", flag.ContinueOnError)
	target := fs.String("url", "", "sse url")
	host := fs.String("host", "", "Host header")
	want := fs.Int("n", 10, "expected events")
	maxGap := fs.Duration("maxgap", 5*time.Second, "max allowed gap between events")
	if err := fs.Parse(args); err != nil {
		return err
	}
	req, err := http.NewRequest(http.MethodGet, *target, nil)
	if err != nil {
		return err
	}
	withHost(req, *host)
	start := time.Now()
	resp, err := httpClient(true).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	sc := bufio.NewScanner(resp.Body)
	got := 0
	last := time.Now()
	var worst time.Duration
	for sc.Scan() {
		if !strings.HasPrefix(sc.Text(), "data:") {
			continue
		}
		got++
		gap := time.Since(last)
		if got > 1 && gap > worst {
			worst = gap
		}
		last = time.Now()
		if got == 1 || got%60 == 0 || got == *want {
			fmt.Printf("sse event %d at %s\n", got, time.Since(start).Round(time.Second))
		}
	}
	fmt.Printf("sse status=%d events=%d worstgap=%s elapsed=%s err=%v\n", resp.StatusCode, got, worst.Round(time.Millisecond), time.Since(start).Round(time.Second), sc.Err())
	if got != *want {
		return fmt.Errorf("got %d events want %d", got, *want)
	}
	if worst > *maxGap {
		return fmt.Errorf("event gap %s exceeds %s (buffering?)", worst, *maxGap)
	}
	return nil
}

func cmdPar(args []string) error {
	fs := flag.NewFlagSet("par", flag.ContinueOnError)
	base := fs.String("url", "", "base url of /gen")
	host := fs.String("host", "", "Host header")
	n := fs.Int("n", 200, "parallel downloads")
	size := fs.String("size", "10M", "bytes each")
	if err := fs.Parse(args); err != nil {
		return err
	}
	sz, err := parseSize(*size)
	if err != nil {
		return err
	}
	cl := httpClient(true)
	var ok, bad atomic.Int64
	var mu sync.Mutex
	var errs []string
	var wg sync.WaitGroup
	start := time.Now()
	for i := range *n {
		wg.Add(1)
		go func(seed uint64) {
			defer wg.Done()
			fail := func(msg string) {
				bad.Add(1)
				mu.Lock()
				if len(errs) < 5 {
					errs = append(errs, msg)
				}
				mu.Unlock()
			}
			req, _ := http.NewRequest(http.MethodGet, fmt.Sprintf("%s?size=%d&seed=%d", *base, sz, seed), nil)
			withHost(req, *host)
			resp, err := cl.Do(req)
			if err != nil {
				fail(err.Error())
				return
			}
			defer resp.Body.Close()
			h := sha256.New()
			got, err := io.Copy(h, resp.Body)
			if err != nil || resp.StatusCode != 200 || got != sz {
				fail(fmt.Sprintf("seed %d status=%d bytes=%d err=%v", seed, resp.StatusCode, got, err))
				return
			}
			if hex.EncodeToString(h.Sum(nil)) != genSha(sz, seed) {
				fail(fmt.Sprintf("seed %d sha mismatch", seed))
				return
			}
			ok.Add(1)
		}(uint64(i + 1000))
	}
	wg.Wait()
	el := time.Since(start)
	fmt.Printf("par ok=%d bad=%d elapsed=%s agg=%.1fMB/s errs=%v\n", ok.Load(), bad.Load(), el.Round(time.Millisecond), float64(ok.Load()*sz)/el.Seconds()/1e6, errs)
	if bad.Load() > 0 {
		return errors.New("some downloads failed")
	}
	return nil
}

func cmdStream(args []string) error {
	fs := flag.NewFlagSet("stream", flag.ContinueOnError)
	api := fs.String("api", "", "control api host:port")
	agent := fs.String("agent", "", "agent id")
	target := fs.String("target", "", "host:port behind the agent")
	token := fs.String("token", os.Getenv("MISHMESH_API_TOKEN"), "bearer token")
	if err := fs.Parse(args); err != nil {
		return err
	}
	conn, err := net.Dial("tcp", *api)
	if err != nil {
		return err
	}
	defer conn.Close()
	q := url.Values{"target": {*target}}
	var b strings.Builder
	fmt.Fprintf(&b, "GET /api/v1/reach/%s/stream?%s HTTP/1.1\r\nHost: %s\r\nConnection: Upgrade\r\nUpgrade: mishmesh-stream\r\n", *agent, q.Encode(), *api)
	if *token != "" {
		fmt.Fprintf(&b, "Authorization: Bearer %s\r\n", *token)
	}
	b.WriteString("\r\n")
	if _, err := io.WriteString(conn, b.String()); err != nil {
		return err
	}
	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, nil)
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusSwitchingProtocols {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("upgrade refused: %s %s", resp.Status, truncate(body))
	}
	done := make(chan struct{})
	go func() {
		_, _ = io.Copy(conn, os.Stdin)
		if tc, ok := conn.(*net.TCPConn); ok {
			_ = tc.CloseWrite()
		}
		close(done)
	}()
	_, err = io.Copy(os.Stdout, br)
	return err
}

func cmdHalfClose(args []string) error {
	fs := flag.NewFlagSet("halfclose", flag.ContinueOnError)
	addr := fs.String("addr", "", "host:port of raw echo-sha service")
	size := fs.String("size", "1M", "bytes to send before shutdown(write)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	n, err := parseSize(*size)
	if err != nil {
		return err
	}
	conn, err := net.DialTimeout("tcp", *addr, 10*time.Second)
	if err != nil {
		return err
	}
	defer conn.Close()
	if _, err := io.Copy(conn, newGen(n, 3)); err != nil {
		return err
	}
	if err := conn.(*net.TCPConn).CloseWrite(); err != nil {
		return err
	}
	_ = conn.SetReadDeadline(time.Now().Add(30 * time.Second))
	reply, err := io.ReadAll(conn)
	if err != nil {
		return fmt.Errorf("reading reply after half-close: %w (got %q)", err, reply)
	}
	want := fmt.Sprintf("got %d bytes sha256=%s\n", n, genSha(n, 3))
	if string(reply) != want {
		return fmt.Errorf("reply %q want %q", reply, want)
	}
	fmt.Print("halfclose ok: ", string(reply))
	return nil
}
