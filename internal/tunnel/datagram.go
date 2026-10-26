package tunnel

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"maps"
	"net"
	"sync"
	"sync/atomic"
	"time"
)

const (
	MaxDatagramSize     = 65507
	DefaultDatagramIdle = 60 * time.Second
	datagramHeaderSize  = 2
	DefaultMaxFlows     = 1024
)

var datagramBufPool = sync.Pool{New: func() any {
	b := make([]byte, MaxDatagramSize)
	return &b
}}

func getDatagramBuf() *[]byte  { return datagramBufPool.Get().(*[]byte) }
func putDatagramBuf(b *[]byte) { datagramBufPool.Put(b) }

var ErrDatagramTooLarge = errors.New("tunnel: datagram too large")

func WriteDatagram(w io.Writer, p []byte) error {
	if len(p) > MaxDatagramSize {
		return ErrDatagramTooLarge
	}
	frame := make([]byte, datagramHeaderSize+len(p))
	binary.BigEndian.PutUint16(frame, uint16(len(p)))
	copy(frame[datagramHeaderSize:], p)
	_, err := w.Write(frame)
	return err
}

func ReadDatagram(r io.Reader, buf []byte) (int, error) {
	var hdr [datagramHeaderSize]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return 0, err
	}
	n := int(binary.BigEndian.Uint16(hdr[:]))
	if n > len(buf) || n > MaxDatagramSize {
		return 0, fmt.Errorf("%w: %d bytes", ErrDatagramTooLarge, n)
	}
	if _, err := io.ReadFull(r, buf[:n]); err != nil {
		return 0, err
	}
	return n, nil
}

func DatagramIdleFromMeta(meta map[string]string) time.Duration {
	d, err := time.ParseDuration(meta[MetaIdleTimeout])
	if err != nil || d <= 0 {
		return DefaultDatagramIdle
	}
	return d
}

func WithDatagramIdle(meta map[string]string, idle time.Duration) map[string]string {
	if idle <= 0 {
		return meta
	}
	out := make(map[string]string, len(meta)+1)
	maps.Copy(out, meta)
	out[MetaIdleTimeout] = idle.String()
	return out
}

type idleWatch struct {
	last atomic.Int64
}

func newIdleWatch() *idleWatch {
	w := &idleWatch{}
	w.touch()
	return w
}

func (w *idleWatch) touch() { w.last.Store(time.Now().UnixNano()) }

func (w *idleWatch) idleFor() time.Duration {
	return time.Duration(time.Now().UnixNano() - w.last.Load())
}

func idleCheckInterval(idle time.Duration) time.Duration {
	return max(idle/4, 10*time.Millisecond)
}

func RelayDatagrams(stream net.Conn, socket net.Conn, idle time.Duration) (fromStream, toStream int64) {
	if idle <= 0 {
		idle = DefaultDatagramIdle
	}
	activity := newIdleWatch()
	var closeOnce sync.Once
	closeBoth := func() {
		closeOnce.Do(func() {
			_ = stream.Close()
			_ = socket.Close()
		})
	}
	var sent, received atomic.Int64
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		defer closeBoth()
		bufp := getDatagramBuf()
		defer putDatagramBuf(bufp)
		buf := *bufp
		for {
			n, err := ReadDatagram(stream, buf)
			if err != nil {
				return
			}
			activity.touch()
			if _, err := socket.Write(buf[:n]); err != nil {
				return
			}
			sent.Add(int64(n))
		}
	}()
	go func() {
		defer wg.Done()
		defer closeBoth()
		bufp := getDatagramBuf()
		defer putDatagramBuf(bufp)
		buf := *bufp
		for {
			n, err := socket.Read(buf)
			if err != nil {
				if isConnRefused(err) {
					continue
				}
				return
			}
			activity.touch()
			if err := WriteDatagram(stream, buf[:n]); err != nil {
				return
			}
			received.Add(int64(n))
		}
	}()

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	tick := time.NewTicker(idleCheckInterval(idle))
	defer tick.Stop()
	for {
		select {
		case <-done:
			return sent.Load(), received.Load()
		case <-tick.C:
			if activity.idleFor() >= idle {
				closeBoth()
			}
		}
	}
}

type DatagramListenerOptions struct {
	Open       func(client net.Addr) (net.Conn, error)
	Idle       time.Duration
	MaxFlows   int
	QueueSize  int
	OnFlowDone func(client net.Addr, fromClient, toClient int64)
	OnDrop     func(reason string)
}

const (
	DropQueueFull = "queue_full"
	DropFlowLimit = "flow_limit"

	defaultDatagramQueue = 64
)

type DatagramListener struct {
	pc   net.PacketConn
	opts DatagramListenerOptions

	mu    sync.Mutex
	flows map[string]*datagramFlow
}

type datagramFlow struct {
	client     net.Addr
	queue      chan []byte
	stop       chan struct{}
	stopOnce   sync.Once
	activity   *idleWatch
	fromClient atomic.Int64
	toClient   atomic.Int64
}

func (f *datagramFlow) close() { f.stopOnce.Do(func() { close(f.stop) }) }

func NewDatagramListener(pc net.PacketConn, opts DatagramListenerOptions) *DatagramListener {
	if opts.Idle <= 0 {
		opts.Idle = DefaultDatagramIdle
	}
	if opts.MaxFlows <= 0 {
		opts.MaxFlows = DefaultMaxFlows
	}
	if opts.QueueSize <= 0 {
		opts.QueueSize = defaultDatagramQueue
	}
	return &DatagramListener{pc: pc, opts: opts, flows: make(map[string]*datagramFlow)}
}

func (l *DatagramListener) ActiveFlows() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.flows)
}

func (l *DatagramListener) Serve() error {
	reaperDone := make(chan struct{})
	defer close(reaperDone)
	go l.reap(reaperDone)
	defer l.closeAll()

	buf := make([]byte, MaxDatagramSize)
	for {
		n, client, err := l.pc.ReadFrom(buf)
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return nil
			}
			if isConnRefused(err) {
				continue
			}
			return err
		}
		packet := make([]byte, n)
		copy(packet, buf[:n])
		l.deliver(client, packet)
	}
}

func (l *DatagramListener) deliver(client net.Addr, packet []byte) {
	if reason := l.enqueue(client, packet); reason != "" {
		l.drop(reason)
	}
}

func (l *DatagramListener) enqueue(client net.Addr, packet []byte) string {
	l.mu.Lock()
	defer l.mu.Unlock()
	key := client.String()
	f, ok := l.flows[key]
	if !ok {
		if len(l.flows) >= l.opts.MaxFlows {
			return DropFlowLimit
		}
		f = &datagramFlow{client: client, queue: make(chan []byte, l.opts.QueueSize), stop: make(chan struct{}), activity: newIdleWatch()}
		l.flows[key] = f
		go l.runFlow(key, f)
	}
	f.activity.touch()
	select {
	case f.queue <- packet:
		return ""
	default:
		return DropQueueFull
	}
}

func (l *DatagramListener) drop(reason string) {
	if l.opts.OnDrop != nil {
		l.opts.OnDrop(reason)
	}
}

func (l *DatagramListener) runFlow(key string, f *datagramFlow) {
	defer l.finishFlow(key, f)
	stream, err := l.opts.Open(f.client)
	if err != nil {
		return
	}
	readerDone := make(chan struct{})
	go func() {
		defer close(readerDone)
		defer f.close()
		bufp := getDatagramBuf()
		defer putDatagramBuf(bufp)
		buf := *bufp
		for {
			n, err := ReadDatagram(stream, buf)
			if err != nil {
				return
			}
			f.activity.touch()
			if _, err := l.pc.WriteTo(buf[:n], f.client); err != nil {
				return
			}
			f.toClient.Add(int64(n))
		}
	}()
	defer func() {
		_ = stream.Close()
		<-readerDone
	}()
	for {
		select {
		case <-f.stop:
			return
		case p := <-f.queue:
			if err := WriteDatagram(stream, p); err != nil {
				return
			}
			f.fromClient.Add(int64(len(p)))
		}
	}
}

func (l *DatagramListener) finishFlow(key string, f *datagramFlow) {
	f.close()
	l.mu.Lock()
	if l.flows[key] == f {
		delete(l.flows, key)
	}
	l.mu.Unlock()
	if l.opts.OnFlowDone != nil {
		l.opts.OnFlowDone(f.client, f.fromClient.Load(), f.toClient.Load())
	}
}

func (l *DatagramListener) reap(done <-chan struct{}) {
	tick := time.NewTicker(idleCheckInterval(l.opts.Idle))
	defer tick.Stop()
	for {
		select {
		case <-done:
			return
		case <-tick.C:
			l.mu.Lock()
			for _, f := range l.flows {
				if f.activity.idleFor() >= l.opts.Idle {
					f.close()
				}
			}
			l.mu.Unlock()
		}
	}
}

func (l *DatagramListener) closeAll() {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, f := range l.flows {
		f.close()
	}
}
