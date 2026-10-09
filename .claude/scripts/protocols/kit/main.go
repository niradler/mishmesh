package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: kit serve|gen|gensha|upload|ws|sse|grpc|par|stream|halfclose ...")
		os.Exit(2)
	}
	cmds := map[string]func([]string) error{
		"serve":     cmdServe,
		"gen":       cmdGen,
		"gensha":    cmdGenSha,
		"upload":    cmdUpload,
		"ws":        cmdWS,
		"sse":       cmdSSE,
		"grpc":      cmdGRPC,
		"par":       cmdPar,
		"stream":    cmdStream,
		"halfclose": cmdHalfClose,
	}
	run, ok := cmds[os.Args[1]]
	if !ok {
		fmt.Fprintf(os.Stderr, "unknown command %q\n", os.Args[1])
		os.Exit(2)
	}
	if err := run(os.Args[2:]); err != nil {
		fmt.Fprintln(os.Stderr, "FAIL:", err)
		os.Exit(1)
	}
}

func parseSize(s string) (int64, error) {
	s = strings.ToUpper(strings.TrimSpace(s))
	mult := int64(1)
	switch {
	case strings.HasSuffix(s, "G"):
		mult, s = 1<<30, strings.TrimSuffix(s, "G")
	case strings.HasSuffix(s, "M"):
		mult, s = 1<<20, strings.TrimSuffix(s, "M")
	case strings.HasSuffix(s, "K"):
		mult, s = 1<<10, strings.TrimSuffix(s, "K")
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("size %q: %w", s, err)
	}
	return n * mult, nil
}
