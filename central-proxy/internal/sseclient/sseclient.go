package sseclient

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"net/http"
	"strings"
	"time"
)

// Client is a minimal SSE client with reconnect and backoff.
type Client struct {
	URL        string
	HTTPClient *http.Client // optional; default no-timeout
	OnMessage  func([]byte) // called with full SSE event payload (concatenated data: lines)
	Headers    map[string]string
}

// Run connects to the SSE endpoint and dispatches concatenated data lines on blank line delimiters.
// It automatically reconnects with exponential backoff (1s -> 30s, capped) and stops when ctx is done.
func (c *Client) Run(ctx context.Context) error {
	if c == nil {
		return errors.New("sseclient: nil Client")
	}

	httpClient := c.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 0}
	}

	const (
		minBackoff = time.Second
		maxBackoff = 30 * time.Second
	)
	backoff := minBackoff

	for {
		// Respect context cancellation before attempting a new connection
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.URL, nil)
		if err != nil {
			if wait(ctx, backoff) != nil {
				return ctx.Err()
			}
			backoff = nextBackoff(backoff, maxBackoff)
			continue
		}
		req.Header.Set("Accept", "text/event-stream")
		for k, v := range c.Headers {
			req.Header.Set(k, v)
		}

		resp, err := httpClient.Do(req)
		if err != nil {
			if wait(ctx, backoff) != nil {
				return ctx.Err()
			}
			backoff = nextBackoff(backoff, maxBackoff)
			continue
		}

		if resp.StatusCode != http.StatusOK {
			_ = resp.Body.Close()
			if wait(ctx, backoff) != nil {
				return ctx.Err()
			}
			backoff = nextBackoff(backoff, maxBackoff)
			continue
		}

		backoff = minBackoff

		reader := bufio.NewReader(resp.Body)
		var payload bytes.Buffer

		for {

			select {
			case <-ctx.Done():
				_ = resp.Body.Close()
				return ctx.Err()
			default:
			}

			line, err := reader.ReadString('\n')
			if len(line) > 0 {
				line = strings.TrimRight(line, "\r\n")
				if line == "" {
					if payload.Len() > 0 {
						b := payload.Bytes()
						if len(b) > 0 && b[len(b)-1] == '\n' {
							b = b[:len(b)-1]
						}
						if c.OnMessage != nil {
							msg := make([]byte, len(b))
							copy(msg, b)
							c.OnMessage(msg)
						}
						payload.Reset()
					}
				} else if strings.HasPrefix(line, ":") {
					// comment line, ignore
				} else if strings.HasPrefix(line, "data:") {
					data := strings.TrimPrefix(line, "data:")
					if len(data) > 0 && data[0] == ' ' {
						data = data[1:]
					}
					payload.WriteString(data)
					payload.WriteByte('\n')
				}
			}

			if err != nil {
				// Connection ended or transient read error: break to reconnect with backoff
				_ = resp.Body.Close()
				break
			}
		}

		if wait(ctx, backoff) != nil {
			return ctx.Err()
		}
		backoff = nextBackoff(backoff, maxBackoff)
	}
}

func nextBackoff(current, max time.Duration) time.Duration {
	next := current * 2
	if next > max {
		return max
	}
	return next
}

func wait(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
