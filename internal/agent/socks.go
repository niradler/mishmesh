package agent

import (
	"bufio"
	"errors"
	"io"
	"net"
	"net/http"
	"strconv"
	"time"

	"github.com/mishmesh/mishmesh/internal/tunnel"
)

const (
	socksVersion        = 0x05
	socksAuthVersion    = 0x01
	socksMethodNone     = 0x00
	socksMethodUserPass = 0x02
	socksNoMethods      = 0xff
	socksCmdConnect     = 0x01
	socksCmdUDPAssoc    = 0x03
	socksAddrIPv4       = 0x01
	socksAddrDomain     = 0x03
	socksAddrIPv6       = 0x04

	socksSucceeded          = 0x00
	socksGeneralFailure     = 0x01
	socksNotAllowed         = 0x02
	socksHostUnreachable    = 0x04
	socksCommandUnsupported = 0x07
	socksAddrUnsupported    = 0x08
)

var (
	errSocksProtocol = errors.New("socks5: protocol error")
	errUDPEgress     = errors.New("udp egress (QUIC, HTTP/3, DNS over UDP) is not supported; only TCP egress is proxied")
)

func (a *Agent) serveSocksClient(client net.Conn) {
	defer client.Close()
	_ = client.SetDeadline(time.Now().Add(egressHandshakeTimeout))
	br := bufio.NewReader(io.LimitReader(client, maxProxyHeaderBytes))
	creds, err := a.socksHandshake(client, br)
	if err != nil {
		return
	}
	dest, code, err := readSocksRequest(br)
	if errors.Is(err, errUDPEgress) {
		a.log.Info("egress refused", "proto", "socks5", "err", err)
	}
	if err != nil {
		if code != 0 {
			writeSocksReply(client, code)
		}
		return
	}
	stream, err := a.openEgress(client, dest, creds)
	if err != nil {
		writeSocksReply(client, socksRefusalCode(err))
		a.log.Info("egress refused", "proto", "socks5", "destination", truncateLog(dest), "err", err)
		return
	}
	defer stream.Close()
	if !writeSocksReply(client, socksSucceeded) {
		return
	}
	_ = client.SetDeadline(time.Time{})
	tunnel.Splice(&bufferedConn{Conn: client, r: bufio.NewReader(io.MultiReader(br, client))}, stream)
}

func (a *Agent) socksHandshake(client net.Conn, br *bufio.Reader) (egressCreds, error) {
	head := make([]byte, 2)
	if _, err := io.ReadFull(br, head); err != nil || head[0] != socksVersion || head[1] == 0 {
		return egressCreds{}, errSocksProtocol
	}
	methods := make([]byte, head[1])
	if _, err := io.ReadFull(br, methods); err != nil {
		return egressCreds{}, errSocksProtocol
	}
	method := byte(socksNoMethods)
	for _, m := range methods {
		if m == socksMethodUserPass {
			method = socksMethodUserPass
			break
		}
		if m == socksMethodNone && !a.opts.Egress.RequireAuth {
			method = socksMethodNone
		}
	}
	if _, err := client.Write([]byte{socksVersion, method}); err != nil || method == socksNoMethods {
		return egressCreds{}, errSocksProtocol
	}
	if method == socksMethodNone {
		return egressCreds{}, nil
	}
	user, pass, err := readSocksUserPass(br)
	if err != nil {
		return egressCreds{}, err
	}
	creds := credsFromUserPass(user, pass)
	if a.opts.Egress.RequireAuth && creds.token == "" {
		_, _ = client.Write([]byte{socksAuthVersion, 0x01})
		return egressCreds{}, errProxyAuthRequired
	}
	if _, err := client.Write([]byte{socksAuthVersion, 0x00}); err != nil {
		return egressCreds{}, err
	}
	return creds, nil
}

func readSocksUserPass(br *bufio.Reader) (string, string, error) {
	ver, err := br.ReadByte()
	if err != nil || ver != socksAuthVersion {
		return "", "", errSocksProtocol
	}
	user, err := readSocksField(br)
	if err != nil {
		return "", "", err
	}
	pass, err := readSocksField(br)
	if err != nil {
		return "", "", err
	}
	return user, pass, nil
}

func readSocksField(br *bufio.Reader) (string, error) {
	n, err := br.ReadByte()
	if err != nil {
		return "", errSocksProtocol
	}
	buf := make([]byte, n)
	if _, err := io.ReadFull(br, buf); err != nil {
		return "", errSocksProtocol
	}
	return string(buf), nil
}

func readSocksRequest(br *bufio.Reader) (string, byte, error) {
	head := make([]byte, 4)
	if _, err := io.ReadFull(br, head); err != nil || head[0] != socksVersion {
		return "", 0, errSocksProtocol
	}
	switch head[1] {
	case socksCmdConnect:
	case socksCmdUDPAssoc:
		return "", socksNotAllowed, errUDPEgress
	default:
		return "", socksCommandUnsupported, errSocksProtocol
	}
	var host string
	switch head[3] {
	case socksAddrIPv4, socksAddrIPv6:
		size := net.IPv4len
		if head[3] == socksAddrIPv6 {
			size = net.IPv6len
		}
		ip := make([]byte, size)
		if _, err := io.ReadFull(br, ip); err != nil {
			return "", 0, errSocksProtocol
		}
		host = net.IP(ip).String()
	case socksAddrDomain:
		name, err := readSocksField(br)
		if err != nil || name == "" {
			return "", socksGeneralFailure, errSocksProtocol
		}
		host = name
	default:
		return "", socksAddrUnsupported, errSocksProtocol
	}
	port := make([]byte, 2)
	if _, err := io.ReadFull(br, port); err != nil {
		return "", 0, errSocksProtocol
	}
	return net.JoinHostPort(host, strconv.Itoa(int(port[0])<<8|int(port[1]))), 0, nil
}

func socksRefusalCode(err error) byte {
	var refusal *egressRefusal
	if !errors.As(err, &refusal) {
		return socksGeneralFailure
	}
	switch refusal.status {
	case http.StatusForbidden, http.StatusProxyAuthRequired:
		return socksNotAllowed
	case http.StatusBadGateway:
		return socksHostUnreachable
	}
	return socksGeneralFailure
}

func writeSocksReply(w io.Writer, code byte) bool {
	_, err := w.Write([]byte{socksVersion, code, 0x00, socksAddrIPv4, 0, 0, 0, 0, 0, 0})
	return err == nil
}
