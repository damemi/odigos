//go:build linux

package websocketserver

import (
	"syscall"

	"golang.org/x/sys/unix"
)

func setTCPUserTimeout(network, address string, c syscall.RawConn) error {
	var sockErr error
	err := c.Control(func(fd uintptr) {
		sockErr = unix.SetsockoptInt(int(fd), unix.IPPROTO_TCP, unix.TCP_USER_TIMEOUT, int(tcpUserTimeout.Milliseconds()))
	})
	if err != nil {
		return err
	}
	return sockErr
}
