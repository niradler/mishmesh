package main

import (
	"bytes"
	"flag"
	"net/http"
	"strconv"
)

func runBackend(args []string) error {
	fs := flag.NewFlagSet("backend", flag.ExitOnError)
	listen := fs.String("listen", "127.0.0.1:9000", "listen address")
	_ = fs.Parse(args)

	small := bytes.Repeat([]byte("x"), 1024)
	medium := bytes.Repeat([]byte("y"), 1<<20)
	mux := http.NewServeMux()
	mux.HandleFunc("/1k", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Length", strconv.Itoa(len(small)))
		_, _ = w.Write(small)
	})
	mux.HandleFunc("/1m", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Length", strconv.Itoa(len(medium)))
		_, _ = w.Write(medium)
	})
	return http.ListenAndServe(*listen, mux)
}
