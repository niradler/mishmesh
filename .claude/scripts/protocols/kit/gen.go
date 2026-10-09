package main

import (
	"crypto/sha256"
	"encoding/hex"
	"flag"
	"fmt"
	"io"
	"math/rand/v2"
	"os"
)

type genReader struct {
	rng       *rand.ChaCha8
	remaining int64
}

func newGen(size int64, seed uint64) *genReader {
	var key [32]byte
	for i := range 8 {
		key[i] = byte(seed >> (8 * i))
	}
	return &genReader{rng: rand.NewChaCha8(key), remaining: size}
}

func (g *genReader) Read(p []byte) (int, error) {
	if g.remaining <= 0 {
		return 0, io.EOF
	}
	if int64(len(p)) > g.remaining {
		p = p[:g.remaining]
	}
	n, _ := g.rng.Read(p)
	g.remaining -= int64(n)
	return n, nil
}

func genSha(size int64, seed uint64) string {
	h := sha256.New()
	buf := make([]byte, 256<<10)
	_, _ = io.CopyBuffer(h, newGen(size, seed), buf)
	return hex.EncodeToString(h.Sum(nil))
}

func genFlags(name string, args []string) (int64, uint64, error) {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	size := fs.String("size", "1M", "bytes, accepts K/M/G")
	seed := fs.Uint64("seed", 1, "generator seed")
	if err := fs.Parse(args); err != nil {
		return 0, 0, err
	}
	n, err := parseSize(*size)
	return n, *seed, err
}

func cmdGen(args []string) error {
	size, seed, err := genFlags("gen", args)
	if err != nil {
		return err
	}
	_, err = io.CopyBuffer(os.Stdout, newGen(size, seed), make([]byte, 256<<10))
	return err
}

func cmdGenSha(args []string) error {
	size, seed, err := genFlags("gensha", args)
	if err != nil {
		return err
	}
	fmt.Println(genSha(size, seed))
	return nil
}
