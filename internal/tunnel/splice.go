package tunnel

import (
	"io"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/hashicorp/yamux"
)

var halfCloseIdleTimeout = 2 * time.Minute

type closeWriter interface {
	CloseWrite() error
}

func CloseWrite(c net.Conn) error {
	switch v := c.(type) {
	case closeWriter:
		return v.CloseWrite()
	case *yamux.Stream:
		return v.Close()
	}
	return errHalfCloseUnsupported
}

type unsupportedError string

func (e unsupportedError) Error() string { return string(e) }

const errHalfCloseUnsupported = unsupportedError("tunnel: half-close unsupported")

type activityWriter struct {
	w    io.Writer
	last *atomic.Int64
}

func (a activityWriter) Write(p []byte) (int, error) {
	n, err := a.w.Write(p)
	if n > 0 {
		a.last.Store(time.Now().UnixNano())
	}
	return n, err
}

func Splice(a, b net.Conn) (aToB, bToA int64) {
	var last atomic.Int64
	last.Store(time.Now().UnixNano())
	var finished atomic.Int32
	var closeOnce sync.Once
	closeBoth := func() {
		closeOnce.Do(func() {
			_ = a.Close()
			_ = b.Close()
		})
	}

	var wg sync.WaitGroup
	pump := func(dst, src net.Conn, n *int64) {
		defer wg.Done()
		copied, err := io.Copy(activityWriter{w: dst, last: &last}, src)
		*n = copied
		finished.Add(1)
		if err != nil {
			closeBoth()
			return
		}
		if cwErr := CloseWrite(dst); cwErr != nil {
			closeBoth()
		}
	}
	wg.Add(2)
	go pump(b, a, &aToB)
	go pump(a, b, &bToA)

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	tick := time.NewTicker(halfCloseIdleTimeout / 4)
	defer tick.Stop()
	for {
		select {
		case <-done:
			closeBoth()
			return aToB, bToA
		case <-tick.C:
			if finished.Load() > 0 && time.Since(time.Unix(0, last.Load())) > halfCloseIdleTimeout {
				closeBoth()
			}
		}
	}
}
