package tunnel

import (
	"errors"
	"syscall"
)

const (
	wsaConnReset   syscall.Errno = 10054
	wsaConnRefused syscall.Errno = 10061
)

func isConnRefused(err error) bool {
	var errno syscall.Errno
	if !errors.As(err, &errno) {
		return false
	}
	switch errno {
	case syscall.ECONNREFUSED, syscall.ECONNRESET, wsaConnReset, wsaConnRefused:
		return true
	}
	return false
}
