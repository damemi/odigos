package gateway

import (
	"context"
	"errors"
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/odigos-io/odigos-enterprise/connectors/base/domain/collectorcredential"
	"github.com/odigos-io/odigos-enterprise/connectors/base/domain/collectorgateway"
)

const (
	testToken       = "a-real-looking-collector-credential"
	testCollectorID = "col-1"
	testCredID      = "cred-1"
	testConnUID     = "uid-1"
	testAccount     = "123456789012"
)

var testNow = time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)

func mustHash(t *testing.T, v string) string {
	t.Helper()
	h, err := collectorcredential.NewSecret(v).Hash()
	if err != nil {
		t.Fatalf("hashing: %v", err)
	}
	return h
}

func activeRecord(t *testing.T) *collectorcredential.Record {
	t.Helper()
	return &collectorcredential.Record{
		CredentialID: testCredID,
		Binding: collectorcredential.Binding{
			CollectorID:  testCollectorID,
			ConnectorUID: testConnUID,
			Provider:     "aws",
			AccountID:    testAccount,
			Region:       "us-east-1",
			NetworkID:    "vpc-1",
		},
		SecretRef:   "odigos/collector/aws-prod/us-east-1/vpc-1",
		TokenSHA256: mustHash(t, testToken),
		State:       collectorcredential.StateActive,
		IssuedAt:    testNow.Add(-time.Hour),
	}
}

func registeredConnector() *collectorgateway.Connector {
	return &collectorgateway.Connector{
		UID:       testConnUID,
		Name:      "aws-prod",
		Namespace: "odigos-system",
		Provider:  "aws",
		AccountID: testAccount,
	}
}

// fakeCredentials stands in for connector_platform.collector_credentials.
type fakeCredentials struct {
	mu sync.Mutex

	record *collectorcredential.Record
	// err, when set, is returned by every read — the "database is down" case, which must not be
	// answered as an authentication failure.
	err error

	activations   []activation
	activationErr error

	// byIDCalls counts session revalidation reads, so a sweep can be shown to scale with credentials
	// rather than with connections.
	byIDCalls int
}

type activation struct {
	collectorID, credentialID string
	at                        time.Time
}

func newFakeCredentials(rec *collectorcredential.Record) *fakeCredentials {
	return &fakeCredentials{record: rec}
}

func (f *fakeCredentials) set(mutate func(*collectorcredential.Record)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	clone := *f.record
	mutate(&clone)
	f.record = &clone
}

func (f *fakeCredentials) setErr(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.err = err
}

func (f *fakeCredentials) lookup() (*collectorcredential.Record, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return nil, f.err
	}
	if f.record == nil {
		return nil, ErrCredentialNotFound
	}
	clone := *f.record
	return &clone, nil
}

func (f *fakeCredentials) ByTokenHash(_ context.Context, tokenHash string) (*collectorcredential.Record, error) {
	rec, err := f.lookup()
	if err != nil {
		return nil, err
	}
	if rec.TokenSHA256 != tokenHash {
		return nil, ErrCredentialNotFound
	}
	return rec, nil
}

func (f *fakeCredentials) ByID(_ context.Context, collectorID, credentialID string) (*collectorcredential.Record, error) {
	f.mu.Lock()
	f.byIDCalls++
	f.mu.Unlock()

	rec, err := f.lookup()
	if err != nil {
		return nil, err
	}
	if rec.Binding.CollectorID != collectorID || rec.CredentialID != credentialID {
		return nil, ErrCredentialNotFound
	}
	return rec, nil
}

func (f *fakeCredentials) MarkActivated(_ context.Context, collectorID, credentialID string, at time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.activationErr != nil {
		return f.activationErr
	}
	f.activations = append(f.activations, activation{collectorID, credentialID, at})
	return nil
}

func (f *fakeCredentials) activationCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.activations)
}

// fakeConnectors stands in for the CR watch.
type fakeConnectors struct {
	mu        sync.Mutex
	connector *collectorgateway.Connector
	synced    bool
}

func newFakeConnectors(c *collectorgateway.Connector) *fakeConnectors {
	return &fakeConnectors{connector: c, synced: true}
}

func (f *fakeConnectors) set(c *collectorgateway.Connector) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.connector = c
}

func (f *fakeConnectors) setSynced(v bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.synced = v
}

func (f *fakeConnectors) ByUID(uid string) (*collectorgateway.Connector, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.connector == nil || f.connector.UID != uid {
		return nil, false
	}
	clone := *f.connector
	return &clone, true
}

func (f *fakeConnectors) HasSynced() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.synced
}

func upstreamTo(raw string) Upstream {
	return func(*collectorgateway.Connector) (*url.URL, error) { return url.Parse(raw) }
}

func upstreamUnresolvable() Upstream {
	return func(*collectorgateway.Connector) (*url.URL, error) {
		return nil, errors.New("no Service exposes an OpAMP port")
	}
}

func (f *fakeCredentials) byIDCallCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.byIDCalls
}
