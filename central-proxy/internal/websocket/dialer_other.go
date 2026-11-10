//go:build !linux

package websocketserver

import "syscall"

// TCP_USER_TIMEOUT is Linux-specific. Other platforms fall back to TCP keepalive
// alone, which is sufficient for local development.
func setTCPUserTimeout(network, address string, c syscall.RawConn) error {
	return nil
}
