// Package anypoint is a minimal REST client for the MuleSoft Anypoint Platform APIs the MuleSoft connector
// needs: the client-credentials token, the caller's identity and environments, the Application Manager (AMC)
// deployments that back CloudHub 2.0 and Runtime Fabric, deployment targets, and Runtime Manager's hybrid
// applications.
//
// It lives in the root connectors module, not in providers/mulesoft, because two consumers need it: the
// connector runtime, and testconnection, which central-backend imports and which provider modules can never
// be imported by. It is stdlib-only on purpose — Anypoint publishes no Go SDK, and keeping this dependency-free
// means onboarding MuleSoft adds nothing to central-backend's module graph.
//
// Endpoint paths and response fields are what the connector reads, not a full model of the API. Paths marked
// "unverified" in a comment come from the platform's public examples rather than its Exchange API reference;
// confirm them there before relying on a field this package does not already parse.
package anypoint

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Control planes, matching the bootstrap regions declared in connectors/spec/data/mulesoft.yaml.
const (
	ControlPlaneUS = "us"
	ControlPlaneEU = "eu"
)

var controlPlaneURLs = map[string]string{
	ControlPlaneUS: "https://anypoint.mulesoft.com",
	ControlPlaneEU: "https://eu1.anypoint.mulesoft.com",
}

// BaseURLFor returns the API base URL of an Anypoint control plane. An override (a non-empty baseURL) wins: it
// is how tests and the local fake server point the client somewhere other than MuleSoft.
func BaseURLFor(controlPlane, override string) (string, error) {
	if override != "" {
		return strings.TrimRight(override, "/"), nil
	}
	if u, ok := controlPlaneURLs[controlPlane]; ok {
		return u, nil
	}
	return "", fmt.Errorf("unsupported Anypoint control plane %q (supported: us, eu)", controlPlane)
}

// Config configures a Client.
type Config struct {
	// BaseURL is the control-plane API root, e.g. https://anypoint.mulesoft.com (see BaseURLFor).
	BaseURL string
	// ClientID and ClientSecret are the Connected App's client-credentials pair.
	ClientID     string
	ClientSecret string
	// HTTPClient is optional; nil uses a client with a 30s timeout.
	HTTPClient *http.Client
	// MaxAttempts bounds retries of throttled (429) and transient (5xx) responses; 0 means 4.
	MaxAttempts int
	// Backoff is the first retry delay, doubled per attempt unless the server sends Retry-After; 0 means 1s.
	Backoff time.Duration
}

// Client calls the Anypoint Platform APIs as a Connected App. It is safe for concurrent use: the access token
// is cached and refreshed under a lock.
type Client struct {
	cfg  Config
	http *http.Client

	mu      sync.Mutex
	token   string
	expires time.Time
}

// New builds a Client. It performs no network I/O; the first API call fetches a token.
func New(cfg Config) *Client {
	cfg.BaseURL = strings.TrimRight(cfg.BaseURL, "/")
	if cfg.MaxAttempts <= 0 {
		cfg.MaxAttempts = 4
	}
	if cfg.Backoff <= 0 {
		cfg.Backoff = time.Second
	}
	hc := cfg.HTTPClient
	if hc == nil {
		hc = &http.Client{Timeout: 30 * time.Second}
	}
	return &Client{cfg: cfg, http: hc}
}

type tokenResponse struct {
	AccessToken string `json:"access_token"`
	ExpiresIn   int    `json:"expires_in"`
	TokenType   string `json:"token_type"`
}

// tokenRefreshMargin renews the token this long before it expires, so a request never carries a token that
// lapses in flight.
const tokenRefreshMargin = time.Minute

// Token returns a valid access token, exchanging the client credentials when there is none or it is about to
// expire. Exposed so a caller can prove the credentials work before making any other call.
func (c *Client) Token(ctx context.Context) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.token != "" && time.Now().Before(c.expires.Add(-tokenRefreshMargin)) {
		return c.token, nil
	}
	return c.fetchTokenLocked(ctx)
}

func (c *Client) invalidateToken() {
	c.mu.Lock()
	c.token = ""
	c.mu.Unlock()
}

func (c *Client) fetchTokenLocked(ctx context.Context) (string, error) {
	form := url.Values{
		"grant_type":    {"client_credentials"},
		"client_id":     {c.cfg.ClientID},
		"client_secret": {c.cfg.ClientSecret},
	}
	const path = "/accounts/api/v2/oauth2/token"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.cfg.BaseURL+path, strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("anypoint token request: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return "", newAPIError(http.MethodPost, path, resp.StatusCode, body)
	}
	var tr tokenResponse
	if err := json.Unmarshal(body, &tr); err != nil {
		return "", fmt.Errorf("anypoint token response: %w", err)
	}
	if tr.AccessToken == "" {
		return "", fmt.Errorf("anypoint token response carried no access_token")
	}
	ttl := time.Duration(tr.ExpiresIn) * time.Second
	if ttl <= 0 {
		ttl = 30 * time.Minute
	}
	c.token = tr.AccessToken
	c.expires = time.Now().Add(ttl)
	return c.token, nil
}

// request is one API call. headers carries the per-call org/environment headers some APIs require.
type request struct {
	method  string
	path    string
	headers map[string]string
	body    any
}

// do executes an authenticated call and decodes a JSON response into out (nil discards it).
//
// A 401 is retried once with a fresh token, because a token can be revoked before its advertised expiry. 429
// and 5xx are retried with backoff, honoring Retry-After: Anypoint throttles per organization, and a discovery
// scan that fans out over many environments is exactly the caller that hits it.
func (c *Client) do(ctx context.Context, r request, out any) error {
	var payload []byte
	if r.body != nil {
		b, err := json.Marshal(r.body)
		if err != nil {
			return fmt.Errorf("encode %s %s: %w", r.method, r.path, err)
		}
		payload = b
	}

	reauthed := false
	delay := c.cfg.Backoff
	for attempt := 1; ; attempt++ {
		token, err := c.Token(ctx)
		if err != nil {
			return err
		}
		var body io.Reader
		if payload != nil {
			body = bytes.NewReader(payload)
		}
		req, err := http.NewRequestWithContext(ctx, r.method, c.cfg.BaseURL+r.path, body)
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Accept", "application/json")
		if payload != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		for k, v := range r.headers {
			req.Header.Set(k, v)
		}

		resp, err := c.http.Do(req)
		if err != nil {
			return fmt.Errorf("%s %s: %w", r.method, r.path, err)
		}
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
		resp.Body.Close()

		switch {
		case resp.StatusCode >= 200 && resp.StatusCode < 300:
			if out == nil || len(bytes.TrimSpace(respBody)) == 0 {
				return nil
			}
			if err := json.Unmarshal(respBody, out); err != nil {
				return fmt.Errorf("decode %s %s: %w", r.method, r.path, err)
			}
			return nil
		case resp.StatusCode == http.StatusUnauthorized && !reauthed:
			reauthed = true
			c.invalidateToken()
			continue
		case (resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500) && attempt < c.cfg.MaxAttempts:
			wait := retryAfter(resp.Header.Get("Retry-After"), delay)
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(wait):
			}
			delay *= 2
			continue
		default:
			return newAPIError(r.method, r.path, resp.StatusCode, respBody)
		}
	}
}

// retryAfter honors a Retry-After header in seconds, falling back to the computed backoff.
func retryAfter(h string, fallback time.Duration) time.Duration {
	if s, err := strconv.Atoi(strings.TrimSpace(h)); err == nil && s >= 0 {
		return time.Duration(s) * time.Second
	}
	return fallback
}

// orgEnvHeaders are the headers Runtime Manager's hybrid API scopes a call by.
func orgEnvHeaders(orgID, envID string) map[string]string {
	return map[string]string{"X-ANYPNT-ORG-ID": orgID, "X-ANYPNT-ENV-ID": envID}
}
