package main

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"math/rand/v2"
	"net"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type secondBucket struct {
	ok  atomic.Int64
	err atomic.Int64
}

type loadResult struct {
	Label      string         `json:"label"`
	Clients    int            `json:"clients"`
	Path       string         `json:"path"`
	DurationS  float64        `json:"duration_s"`
	Requests   int            `json:"requests"`
	Errors     int            `json:"errors"`
	ErrorRate  float64        `json:"error_rate"`
	ReqPerSec  float64        `json:"req_per_s"`
	MBPerSec   float64        `json:"mb_per_s"`
	P50Ms      float64        `json:"p50_ms"`
	P95Ms      float64        `json:"p95_ms"`
	P99Ms      float64        `json:"p99_ms"`
	MaxMs      float64        `json:"max_ms"`
	ErrKinds   map[string]int `json:"err_kinds,omitempty"`
	StartMilli int64          `json:"start_ms"`
}

func runLoad(args []string) error {
	fs := flag.NewFlagSet("load", flag.ExitOnError)
	addr := fs.String("addr", "server-a:8080", "ingress host:port to connect to")
	suffix := fs.String("host-suffix", "lt.test", "base domain used in Host header")
	prefix := fs.String("prefix", "p", "subdomain prefix")
	agentsN := fs.Int("agents", 100, "number of agent subdomains to spread across")
	direct := fs.Bool("direct", false, "hit addr directly (no Host spreading)")
	path := fs.String("path", "/1k", "request path")
	size := fs.Int("size", 1024, "expected response bytes")
	clients := fs.Int("c", 50, "concurrent clients")
	dur := fs.Duration("d", 20*time.Second, "test duration")
	warm := fs.Duration("warmup", 3*time.Second, "warmup excluded from stats")
	timeline := fs.String("timeline", "", "CSV per-second ok/err timeline")
	label := fs.String("label", "", "label for the JSON result")
	reqTimeout := fs.Duration("timeout", 30*time.Second, "per request timeout")
	idxFrom := fs.Int("agent-from", 0, "first agent index")
	_ = fs.Parse(args)

	dialer := &net.Dialer{Timeout: 5 * time.Second}
	tr := &http.Transport{
		DialContext:         func(ctx context.Context, network, _ string) (net.Conn, error) { return dialer.DialContext(ctx, network, *addr) },
		MaxIdleConns:        *clients * 2,
		MaxIdleConnsPerHost: *clients * 2,
		MaxConnsPerHost:     0,
		IdleConnTimeout:     60 * time.Second,
		DisableCompression:  true,
	}
	client := &http.Client{Transport: tr, Timeout: *reqTimeout}

	start := time.Now()
	measureFrom := start.Add(*warm)
	end := start.Add(*warm + *dur)
	nBuckets := int((*warm+*dur)/time.Second) + 2
	buckets := make([]secondBucket, nBuckets)

	type workerOut struct {
		lat   []time.Duration
		errs  map[string]int
		bytes int64
	}
	outs := make([]workerOut, *clients)
	var wg sync.WaitGroup
	for w := 0; w < *clients; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			o := &outs[w]
			o.errs = map[string]int{}
			rnd := rand.New(rand.NewPCG(uint64(w)+1, uint64(time.Now().UnixNano())))
			for time.Now().Before(end) {
				host := *suffix
				if !*direct {
					host = fmt.Sprintf("%s%d.%s", *prefix, *idxFrom+rnd.IntN(*agentsN), *suffix)
				}
				t0 := time.Now()
				req, _ := http.NewRequest(http.MethodGet, "http://"+*addr+*path, nil)
				req.Host = host
				resp, err := client.Do(req)
				var n int64
				if err == nil {
					n, err = io.Copy(io.Discard, resp.Body)
					resp.Body.Close()
					if err == nil && resp.StatusCode != http.StatusOK {
						err = fmt.Errorf("status %d", resp.StatusCode)
					} else if err == nil && n != int64(*size) {
						err = fmt.Errorf("short body %d", n)
					}
				}
				lat := time.Since(t0)
				bi := int(time.Since(start) / time.Second)
				if bi >= len(buckets) {
					bi = len(buckets) - 1
				}
				if err != nil {
					buckets[bi].err.Add(1)
				} else {
					buckets[bi].ok.Add(1)
				}
				if t0.Before(measureFrom) {
					if err != nil {
						time.Sleep(5 * time.Millisecond)
					}
					continue
				}
				if err != nil {
					o.errs[errKind(err)]++
					time.Sleep(5 * time.Millisecond)
					continue
				}
				o.lat = append(o.lat, lat)
				o.bytes += n
			}
		}(w)
	}
	wg.Wait()

	var all []time.Duration
	errs := map[string]int{}
	var bytes int64
	for _, o := range outs {
		all = append(all, o.lat...)
		bytes += o.bytes
		for k, v := range o.errs {
			errs[k] += v
		}
	}
	sort.Slice(all, func(i, j int) bool { return all[i] < all[j] })
	errTotal := 0
	for _, v := range errs {
		errTotal += v
	}
	total := len(all) + errTotal
	res := loadResult{
		Label: *label, Clients: *clients, Path: *path, DurationS: dur.Seconds(),
		Requests: total, Errors: errTotal,
		ReqPerSec: float64(len(all)) / dur.Seconds(),
		MBPerSec:  float64(bytes) / dur.Seconds() / (1 << 20),
		P50Ms:     ms(percentile(all, 0.5)), P95Ms: ms(percentile(all, 0.95)), P99Ms: ms(percentile(all, 0.99)),
		MaxMs: ms(percentile(all, 1)), ErrKinds: errs, StartMilli: measureFrom.UnixMilli(),
	}
	if total > 0 {
		res.ErrorRate = float64(errTotal) / float64(total)
	}
	b, _ := json.Marshal(res)
	fmt.Println(string(b))

	if *timeline != "" {
		f, err := os.Create(*timeline)
		if err != nil {
			return err
		}
		defer f.Close()
		w := bufio.NewWriter(f)
		fmt.Fprintln(w, "ts_ms,ok,err")
		for i := range buckets {
			fmt.Fprintf(w, "%d,%d,%d\n", start.Add(time.Duration(i)*time.Second).UnixMilli(), buckets[i].ok.Load(), buckets[i].err.Load())
		}
		_ = w.Flush()
	}
	return nil
}

func errKind(err error) string {
	s := err.Error()
	switch {
	case strings.Contains(s, "status"):
		return s
	case strings.Contains(s, "refused"):
		return "conn refused"
	case strings.Contains(s, "reset"):
		return "conn reset"
	case strings.Contains(s, "EOF"):
		return "EOF"
	case strings.Contains(s, "timeout") || strings.Contains(s, "deadline"):
		return "timeout"
	case strings.Contains(s, "short body"):
		return "short body"
	}
	if len(s) > 60 {
		s = s[:60]
	}
	return s
}
