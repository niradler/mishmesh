package tunnel

import (
	"io"
	"net"
	"sync"
	"time"

	"github.com/hashicorp/yamux"
)

type Session struct {
	ymx  *yamux.Session
	conn net.Conn
}

func yamuxConfig() *yamux.Config {
	cfg := yamux.DefaultConfig()
	cfg.LogOutput = io.Discard
	cfg.EnableKeepAlive = true
	return cfg
}

func Server(conn net.Conn) (*Session, error) {
	ymx, err := yamux.Server(conn, yamuxConfig())
	if err != nil {
		return nil, err
	}
	return &Session{ymx: ymx, conn: conn}, nil
}

func Client(conn net.Conn) (*Session, error) {
	ymx, err := yamux.Client(conn, yamuxConfig())
	if err != nil {
		return nil, err
	}
	return &Session{ymx: ymx, conn: conn}, nil
}

func (s *Session) OpenControl() (*Control, error) {
	stream, err := s.ymx.OpenStream()
	if err != nil {
		return nil, err
	}
	return newControl(stream), nil
}

func (s *Session) AcceptControl() (*Control, error) {
	stream, err := s.ymx.AcceptStream()
	if err != nil {
		return nil, err
	}
	return newControl(stream), nil
}

func (s *Session) OpenData(init StreamInit) (net.Conn, error) {
	stream, err := s.ymx.OpenStream()
	if err != nil {
		return nil, err
	}
	if err := writeJSON(stream, init); err != nil {
		_ = stream.Close()
		return nil, err
	}
	return stream, nil
}

const InitReadTimeout = 10 * time.Second

func (s *Session) AcceptData() (net.Conn, StreamInit, error) {
	stream, err := s.AcceptStream()
	if err != nil {
		return nil, StreamInit{}, err
	}
	init, err := ReadStreamInit(stream)
	if err != nil {
		return nil, StreamInit{}, err
	}
	return stream, init, nil
}

func (s *Session) AcceptStream() (net.Conn, error) {
	return s.ymx.AcceptStream()
}

func ReadStreamInit(stream net.Conn) (StreamInit, error) {
	_ = stream.SetReadDeadline(time.Now().Add(InitReadTimeout))
	var init StreamInit
	if err := readJSON(stream, &init); err != nil {
		_ = stream.Close()
		return StreamInit{}, err
	}
	_ = stream.SetReadDeadline(time.Time{})
	return init, nil
}

func (s *Session) CloseChan() <-chan struct{} { return s.ymx.CloseChan() }

func (s *Session) Close() error { return s.ymx.Close() }

type Control struct {
	conn   net.Conn
	writeM sync.Mutex
}

func newControl(conn net.Conn) *Control { return &Control{conn: conn} }

func (c *Control) Send(msg ControlMessage) error {
	c.writeM.Lock()
	defer c.writeM.Unlock()
	return writeJSON(c.conn, msg)
}

func (c *Control) Recv() (ControlMessage, error) {
	var msg ControlMessage
	if err := readJSON(c.conn, &msg); err != nil {
		return ControlMessage{}, err
	}
	return msg, nil
}

func (c *Control) SetReadDeadline(t time.Time) error { return c.conn.SetReadDeadline(t) }

func (c *Control) Close() error { return c.conn.Close() }
