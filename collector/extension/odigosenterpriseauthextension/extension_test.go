package odigosenterpriseauthextension

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/component/componentstatus"
	"go.uber.org/zap"
)

// statusHost captures fatal-error events the extension reports.
type statusHost struct {
	events chan *componentstatus.Event
}

func newStatusHost() *statusHost {
	return &statusHost{events: make(chan *componentstatus.Event, 1)}
}

func (h *statusHost) GetExtensions() map[component.ID]component.Component { return nil }

func (h *statusHost) Report(e *componentstatus.Event) {
	select {
	case h.events <- e:
	default:
	}
}

func newTestExtension(t *testing.T) *authExtension {
	t.Helper()
	return newAuthExtension(zap.NewNop())
}

func TestStart_MissingTokenFailsStartup(t *testing.T) {
	t.Setenv(OnPremTokenEnvVar, "")

	err := newTestExtension(t).Start(context.Background(), newStatusHost())

	require.Error(t, err, "collector must refuse to start without a token")
	assert.Contains(t, err.Error(), OnPremTokenEnvVar)
}

func TestStart_WhitespaceOnlyTokenFailsStartup(t *testing.T) {
	// Tokens pasted into secrets routinely pick up a trailing newline; make sure a value that is
	// only whitespace is treated as absent rather than handed to the parser.
	t.Setenv(OnPremTokenEnvVar, "   \n\t ")

	err := newTestExtension(t).Start(context.Background(), newStatusHost())

	require.Error(t, err)
	assert.Contains(t, err.Error(), OnPremTokenEnvVar)
}

func TestStart_InvalidTokenFailsStartup(t *testing.T) {
	t.Setenv(OnPremTokenEnvVar, "not-a-jwt")

	ext := newTestExtension(t)
	ext.verifyToken = func(string) (string, error) { return "", errors.New("bad signature") }

	err := ext.Start(context.Background(), newStatusHost())

	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to verify Odigos on-prem token")
}

func TestStart_ValidTokenStartsCleanly(t *testing.T) {
	t.Setenv(OnPremTokenEnvVar, "valid-token")

	ext := newTestExtension(t)
	ext.verifyToken = func(string) (string, error) { return "acme-corp", nil }
	ext.expirationChannel = func() (<-chan time.Time, error) { return make(chan time.Time), nil }

	require.NoError(t, ext.Start(context.Background(), newStatusHost()))
	require.NoError(t, ext.Shutdown(context.Background()))
}

func TestExpiredTokenReportsFatalError(t *testing.T) {
	t.Setenv(OnPremTokenEnvVar, "valid-now-expires-later")

	expiry := make(chan time.Time, 1)
	ext := newTestExtension(t)
	ext.verifyToken = func(string) (string, error) { return "acme-corp", nil }
	ext.expirationChannel = func() (<-chan time.Time, error) { return expiry, nil }

	host := newStatusHost()
	require.NoError(t, ext.Start(context.Background(), host))

	expiry <- time.Now()

	select {
	case event := <-host.events:
		assert.Equal(t, componentstatus.StatusFatalError, event.Status())
		assert.ErrorIs(t, event.Err(), ErrTokenExpired)
	case <-time.After(5 * time.Second):
		t.Fatal("expected a fatal error event when the token expires")
	}

	require.NoError(t, ext.Shutdown(context.Background()))
}

func TestShutdownStopsExpirationWatcher(t *testing.T) {
	t.Setenv(OnPremTokenEnvVar, "valid-token")

	expiry := make(chan time.Time, 1)
	ext := newTestExtension(t)
	ext.verifyToken = func(string) (string, error) { return "acme-corp", nil }
	ext.expirationChannel = func() (<-chan time.Time, error) { return expiry, nil }

	host := newStatusHost()
	require.NoError(t, ext.Start(context.Background(), host))
	require.NoError(t, ext.Shutdown(context.Background()))

	// After shutdown the watcher must be gone, so an expiry firing is not reported.
	expiry <- time.Now()

	select {
	case event := <-host.events:
		t.Fatalf("expected no event after shutdown, got %v", event.Status())
	case <-time.After(250 * time.Millisecond):
	}
}

func TestShutdownBeforeStartIsSafe(t *testing.T) {
	assert.NoError(t, newTestExtension(t).Shutdown(context.Background()))
}
