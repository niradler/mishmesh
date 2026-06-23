package sshfwd

import (
	"net"
	"time"

	"golang.org/x/crypto/ssh"
)

type chanAddr string

func (a chanAddr) Network() string { return "ssh" }
func (a chanAddr) String() string  { return string(a) }

type channelConn struct {
	ssh.Channel
	local  net.Addr
	remote net.Addr
}

func newChannelConn(ch ssh.Channel, local, remote string) *channelConn {
	return &channelConn{Channel: ch, local: chanAddr(local), remote: chanAddr(remote)}
}

func (c *channelConn) LocalAddr() net.Addr                { return c.local }
func (c *channelConn) RemoteAddr() net.Addr               { return c.remote }
func (c *channelConn) SetDeadline(t time.Time) error      { return nil }
func (c *channelConn) SetReadDeadline(t time.Time) error  { return nil }
func (c *channelConn) SetWriteDeadline(t time.Time) error { return nil }
