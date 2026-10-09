package main

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/pprof"
	"os"
	"os/signal"
	"reflect"
	"runtime"
	"strconv"
	"syscall"
	"time"
	"unsafe"

	"github.com/mishmesh/mishmesh/internal/config"
	"github.com/mishmesh/mishmesh/internal/controlplane"
	"github.com/mishmesh/mishmesh/internal/gateway"
	"github.com/mishmesh/mishmesh/internal/ingress"
	"github.com/mishmesh/mishmesh/internal/metrics"
	"github.com/mishmesh/mishmesh/internal/store"
	"github.com/mishmesh/mishmesh/internal/store/memory"
	"github.com/mishmesh/mishmesh/internal/store/postgres"
	"github.com/mishmesh/mishmesh/internal/store/redis"
	"github.com/mishmesh/mishmesh/internal/store/sqlite"
	"github.com/mishmesh/mishmesh/internal/tunnel"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run() error {
	cfg := config.LoadServer()
	if err := cfg.Validate(); err != nil {
		return err
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

	var data store.DataStore
	var err error
	if cfg.EffectiveDataBackend() == "postgres" {
		data, err = postgres.Open(cfg.DataDSN, postgres.PoolConfig{})
	} else {
		data, err = sqlite.Open(cfg.DataDSN)
	}
	if err != nil {
		return err
	}
	defer data.Close()
	if n, convErr := strconv.Atoi(os.Getenv("LT_PG_POOL")); convErr == nil && n > 0 {
		tunePool(data, n)
	}

	var conns store.ConnectionStore = memory.NewConnStore()
	if cfg.ConnBackend == "redis" {
		conns, err = redis.NewConnStore(cfg.RedisURL, cfg.RedisPoolSize)
		if err != nil {
			return err
		}
	}

	mx := metrics.New()
	gw := gateway.New(gateway.Options{
		Data: data, Conns: conns, Log: log, BaseDomain: cfg.BaseDomain, PublicScheme: cfg.PublicScheme, Metrics: mx,
	})
	apiMux := http.NewServeMux()
	apiMux.HandleFunc(tunnel.AgentConnectPath, gw.HandleAgentConnect)
	cp := controlplane.New(data, conns, cfg.APIAuthToken, log)
	cp.SetPublicConfig(cfg.BaseDomain, cfg.PublicScheme)
	cp.Register(apiMux)
	apiMux.Handle("GET /metrics", mx.Handler())
	apiMux.HandleFunc("/debug/pprof/", pprof.Index)
	apiMux.HandleFunc("/debug/pprof/profile", pprof.Profile)
	apiMux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	apiMux.HandleFunc("/debug/pprof/trace", pprof.Trace)
	apiMux.HandleFunc("GET /debug/rt", func(w http.ResponseWriter, _ *http.Request) {
		var ms runtime.MemStats
		runtime.ReadMemStats(&ms)
		if poolDB != nil {
			st := poolDB.Stats()
			fmt.Fprintf(w, "pool_open %d inuse %d idle %d waits %d wait_ms %d\n", st.OpenConnections, st.InUse, st.Idle, st.WaitCount, st.WaitDuration.Milliseconds())
		}
		fmt.Fprintf(w, "goroutines %d\nheap_alloc %d\nheap_inuse %d\nheap_sys %d\nstack_inuse %d\nsys %d\nnum_gc %d\n",
			runtime.NumGoroutine(), ms.HeapAlloc, ms.HeapInuse, ms.HeapSys, ms.StackInuse, ms.Sys, ms.NumGC)
	})

	ing := ingress.New(ingress.Options{Data: data, Conns: conns, Log: log, BaseDomain: cfg.BaseDomain, Meter: mx})
	servers := []*http.Server{
		{Addr: cfg.APIAddr, Handler: apiMux},
		{Addr: cfg.IngressAddr, Handler: ing},
	}
	for _, s := range servers {
		go func(s *http.Server) {
			if err := s.ListenAndServe(); err != nil && err != http.ErrServerClosed {
				fmt.Fprintln(os.Stderr, "listen:", err)
				os.Exit(1)
			}
		}(s)
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	<-ctx.Done()
	shut, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for _, s := range servers {
		_ = s.Shutdown(shut)
	}
	return nil
}

var poolDB *sql.DB

func tunePool(data store.DataStore, n int) {
	field := reflect.ValueOf(data).Elem().FieldByName("db")
	db := *(**sql.DB)(unsafe.Pointer(field.UnsafeAddr()))
	poolDB = db
	db.SetMaxOpenConns(n)
	db.SetMaxIdleConns(n)
	db.SetConnMaxLifetime(0)
}
