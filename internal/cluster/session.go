package cluster

import (
	"io"
	"time"

	"github.com/hashicorp/yamux"
)

func yamuxConfig() *yamux.Config {
	cfg := yamux.DefaultConfig()
	cfg.LogOutput = io.Discard
	cfg.EnableKeepAlive = true
	cfg.KeepAliveInterval = 15 * time.Second
	cfg.ConnectionWriteTimeout = 10 * time.Second
	cfg.MaxStreamWindowSize = 1 << 20
	cfg.AcceptBacklog = 1024
	return cfg
}
