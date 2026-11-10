package websocketserver

import (
	"crypto/tls"
	"net"
	"net/http"
	"time"
)

// tcpUserTimeout bounds how long the kernel keeps retransmitting unacknowledged
// data before failing the socket. The default (tcp_retries2=15) is roughly 15
// minutes, which is how long a silently broken path stays undetected when the
// application-level ping is unable to fire.
const tcpUserTimeout = 60 * time.Second

// newHTTPClient builds the client used for the WebSocket handshake. Beyond TLS it
// configures TCP-level liveness so a dead path surfaces even if the application
// ping is starved by a blocked write.
func newHTTPClient(tlsConfig *tls.Config) *http.Client {
	dialer := &net.Dialer{
		Timeout: 10 * time.Second,
		KeepAliveConfig: net.KeepAliveConfig{
			Enable:   true,
			Idle:     15 * time.Second,
			Interval: 15 * time.Second,
			Count:    4,
		},
		Control: setTCPUserTimeout,
	}

	return &http.Client{
		Transport: &http.Transport{
			TLSNextProto:    map[string]func(string, *tls.Conn) http.RoundTripper{},
			TLSClientConfig: tlsConfig,
			DialContext:     dialer.DialContext,
		},
	}
}
