// Package central connects the connector runtime to Odigos Central over a reverse WebSocket and serves
// the agents-api GraphQL schema over it — the exact mechanism the VM agent uses. Central dials no
// inbound connection to the connector; instead the connector registers at /ws/proxy with
// platformType=connector, and Central routes agents-api queries to it over this socket. This makes a
// connector surface and be queried identically to a VM agent (no Kubernetes/compute-platform binding).
package central

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"net/url"
	"strings"
	"time"

	"github.com/99designs/gqlgen/graphql/executor"
	"github.com/coder/websocket"

	"github.com/odigos-io/odigos-enterprise/connectors/base/gql"
	connlog "github.com/odigos-io/odigos-enterprise/connectors/base/log"
)

var centralLog = connlog.Subsystem("central")

const (
	webSocketPath  = "/ws/proxy"
	maxMessageSize = 10 * 1024 * 1024 // 10 MB, matching the VM agent

	msgTypeGraphQLQuery    = "graphql_query"
	msgTypeGraphQLResponse = "graphql_response"

	// pingInterval/pingTimeout drive an application-level keepalive: without it a silent TCP drop (NAT/LB
	// idle timeout, node blip) would leave conn.Read blocked until the OS TCP keepalive fires — potentially
	// hours — so the connector would believe it is still connected while Central sees it gone (split-brain).
	// A failed ping cancels the connection so Run reconnects promptly.
	pingInterval = 20 * time.Second
	pingTimeout  = 10 * time.Second

	// dialTimeout bounds a single connection attempt's handshake. Without it, a half-open network path
	// (LB accepts the TCP connection but never completes the WS upgrade) would block the dial until the OS
	// TCP timeout — potentially minutes — during which Run can neither back off nor retry. coder/websocket
	// only uses the dial context for the handshake, so it is safe to cancel once Dial returns.
	dialTimeout = 30 * time.Second

	// maxReconnectDelay caps the exponential reconnect backoff so a Central outage doesn't produce a
	// thundering herd of connectors all retrying on the same fixed interval.
	maxReconnectDelay = 2 * time.Minute
)

// messageFromCentral is a frame Central sends to the connector (a GraphQL query to execute).
type messageFromCentral struct {
	Type      string         `json:"type"`
	Query     string         `json:"query,omitempty"`
	Variables map[string]any `json:"variables,omitempty"`
	RequestID string         `json:"request_id,omitempty"`
	// IsAdmin is the calling user's admin flag, forwarded by Central per frame (same as for k8s/VM
	// proxies). The executor requires it for mutations; a non-admin may read but not mutate.
	IsAdmin bool `json:"is_admin,omitempty"`
}

// messageToCentral is a frame the connector sends back (the GraphQL response).
type messageToCentral struct {
	Type      string `json:"type"`
	RequestID string `json:"request_id"`
	Data      any    `json:"data"`
}

// Config configures the central WebSocket client.
type Config struct {
	// BackendURL is the central-backend base URL (http(s):// or ws(s)://, host[:port]); empty disables.
	BackendURL string
	// ProxyID is the stable connection id (the connector name).
	ProxyID string
	// PlatformName is the display name shown in Central's platform list.
	PlatformName string
	// Version is the connector runtime version.
	Version string
	// SchemaVersion is the agents-api schema version reported at registration.
	SchemaVersion string
	// ReconnectDelay is the backoff between connection attempts.
	ReconnectDelay time.Duration
}

type Client struct {
	cfg  Config
	exec *executor.Executor
}

func New(cfg Config, exec *executor.Executor) *Client {
	if cfg.ReconnectDelay == 0 {
		cfg.ReconnectDelay = 5 * time.Second
	}
	return &Client{cfg: cfg, exec: exec}
}

// Run maintains the connection to Central until ctx is cancelled, reconnecting with exponential backoff
// (reset after a successful session) plus jitter so an outage doesn't cause synchronized reconnects.
func (c *Client) Run(ctx context.Context) {
	attempt := 0
	for {
		if ctx.Err() != nil {
			return
		}
		connected, err := c.connectAndServe(ctx)
		if ctx.Err() != nil {
			return
		}
		if connected {
			attempt = 0 // a session was established; reset backoff so a normal drop reconnects quickly
		}
		delay := c.reconnectDelay(attempt)
		attempt++
		if err != nil {
			centralLog.Warn("connection lost, reconnecting", "error", err, "delay", delay, "attempt", attempt)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(delay):
		}
	}
}

// reconnectDelay returns an exponentially increasing delay (base = ReconnectDelay, capped at
// maxReconnectDelay) with up to +25% jitter to de-synchronize a fleet's reconnects.
func (c *Client) reconnectDelay(attempt int) time.Duration {
	delay := c.cfg.ReconnectDelay
	for i := 0; i < attempt && delay < maxReconnectDelay; i++ {
		delay *= 2
	}
	if delay > maxReconnectDelay {
		delay = maxReconnectDelay
	}
	jitter := time.Duration(rand.Int63n(int64(delay)/4 + 1))
	return delay + jitter
}

// connectAndServe dials Central and serves the reverse-WebSocket until the connection drops or ctx is
// cancelled. It returns whether a connection was established (used to reset reconnect backoff) and the
// error that ended the session.
func (c *Client) connectAndServe(ctx context.Context) (connected bool, err error) {
	u, err := c.wsURL()
	if err != nil {
		return false, err
	}
	centralLog.Info("dialing central backend", "url", u)
	dialCtx, dialCancel := context.WithTimeout(ctx, dialTimeout)
	conn, _, err := websocket.Dial(dialCtx, u, nil)
	dialCancel()
	if err != nil {
		return false, fmt.Errorf("dial %s: %w", u, err)
	}
	defer conn.Close(websocket.StatusNormalClosure, "shutting down")
	conn.SetReadLimit(maxMessageSize)
	centralLog.Info("connected to central", "proxy_id", c.cfg.ProxyID, "platform_type", "connector")

	// connCtx is cancelled when the keepalive detects a dead peer, unblocking the read loop below so Run
	// reconnects instead of blocking on a silently-dropped socket.
	connCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	go c.keepAlive(connCtx, conn, cancel)

	for {
		_, data, readErr := conn.Read(connCtx)
		if readErr != nil {
			return true, fmt.Errorf("read: %w", readErr)
		}
		c.handleMessage(connCtx, conn, data)
	}
}

// keepAlive pings Central on an interval; a ping that fails (or times out) means the peer is gone, so it
// cancels the connection to trigger a reconnect. coder/websocket answers inbound pings from within Read,
// so this outbound ping is what detects a silent drop.
func (c *Client) keepAlive(ctx context.Context, conn *websocket.Conn, cancel context.CancelFunc) {
	ticker := time.NewTicker(pingInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			pingCtx, pcancel := context.WithTimeout(ctx, pingTimeout)
			err := conn.Ping(pingCtx)
			pcancel()
			if err != nil {
				if ctx.Err() == nil {
					centralLog.Warn("central keepalive ping failed; dropping connection", "error", err)
				}
				cancel()
				return
			}
		}
	}
}

func (c *Client) wsURL() (string, error) {
	raw := c.cfg.BackendURL
	scheme := "ws"
	if i := strings.Index(raw, "://"); i >= 0 {
		switch raw[:i] {
		case "https", "wss":
			scheme = "wss"
		default:
			scheme = "ws"
		}
		raw = raw[i+3:]
	}
	u := &url.URL{Scheme: scheme, Host: raw, Path: webSocketPath}
	q := u.Query()
	q.Set("proxyID", c.cfg.ProxyID)
	q.Set("platformName", c.cfg.PlatformName)
	q.Set("version", c.cfg.Version)
	q.Set("platformType", "connector")
	q.Set("graphqlSchemaVersion", c.cfg.SchemaVersion)
	u.RawQuery = q.Encode()
	return u.String(), nil
}

func (c *Client) handleMessage(ctx context.Context, conn *websocket.Conn, data []byte) {
	var msg messageFromCentral
	if err := json.Unmarshal(data, &msg); err != nil {
		centralLog.Error("bad central message", "error", err)
		return
	}
	if msg.Type != msgTypeGraphQLQuery {
		centralLog.Debug("ignoring central message", "type", msg.Type)
		return
	}
	op := gql.ExtractOperationName(msg.Query)
	centralLog.Debug("central graphql query", "request_id", msg.RequestID, "operation", op)
	resp := gql.RunOperation(ctx, c.exec, msg.Query, msg.Variables, "central", msg.IsAdmin)
	out, err := json.Marshal(messageToCentral{Type: msgTypeGraphQLResponse, RequestID: msg.RequestID, Data: resp})
	if err != nil {
		centralLog.Error("marshal central response failed", "request_id", msg.RequestID, "error", err)
		return
	}
	if err := conn.Write(ctx, websocket.MessageText, out); err != nil {
		centralLog.Error("write central response failed", "request_id", msg.RequestID, "error", err)
	}
}
