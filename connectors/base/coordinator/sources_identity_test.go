package coordinator

import (
	"context"
	"testing"

	"github.com/odigos-io/odigos-enterprise/connectors/base/config"
	domainsrc "github.com/odigos-io/odigos-enterprise/connectors/base/domain/source"
	"github.com/odigos-io/odigos-enterprise/connectors/base/store"
)

// A source's identity is (kind, external_resource_id) — the stable cloud resource id, NOT the source_name
// handle, the mutable service_name override, or region. An update that changes an identity component is a
// move that must delete the old-key row to avoid an orphan; a change to only non-key fields (service_name,
// region, enabled, config) is an in-place update.
func TestSourceIdentityChanged(t *testing.T) {
	base := store.SourceRecord{Kind: "aws.lambda", ServiceName: "svc", Region: "us-east-1", ExternalResourceID: "arn:aws:lambda:us-east-1:1:function:f"}
	cases := []struct {
		name    string
		desired domainsrc.Source
		moved   bool
	}{
		{
			name:    "same identity, enabled toggled",
			desired: domainsrc.Source{Kind: "aws.lambda", ServiceName: "svc", Region: "us-east-1", ExternalResourceID: base.ExternalResourceID, Enabled: true},
			moved:   false,
		},
		{
			name:    "service-name override changed is in-place (not a move)",
			desired: domainsrc.Source{Kind: "aws.lambda", ServiceName: "renamed", Region: "us-east-1", ExternalResourceID: base.ExternalResourceID},
			moved:   false,
		},
		{
			name:    "region changed is in-place (not a move)",
			desired: domainsrc.Source{Kind: "aws.lambda", ServiceName: "svc", Region: "eu-west-1", ExternalResourceID: base.ExternalResourceID},
			moved:   false,
		},
		{
			name:    "external resource id changed",
			desired: domainsrc.Source{Kind: "aws.lambda", ServiceName: "svc", Region: "us-east-1", ExternalResourceID: "arn:aws:lambda:us-east-1:1:function:other"},
			moved:   true,
		},
		{
			name:    "kind changed",
			desired: domainsrc.Source{Kind: "aws.fargate-task", ServiceName: "svc", Region: "us-east-1", ExternalResourceID: base.ExternalResourceID},
			moved:   true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := sourceIdentityChanged(base, tc.desired); got != tc.moved {
				t.Errorf("sourceIdentityChanged = %v, want %v", got, tc.moved)
			}
		})
	}
}

func TestRevalidateWorkloadsTriggersAutomaticAndManualPaths(t *testing.T) {
	c := New(config.Config{}, config.NewManager(&memoryConfigStore{}, config.Defaults()), nil, fakeProvider{})
	if err := c.RevalidateWorkloads(context.Background()); err != nil {
		t.Fatalf("RevalidateWorkloads: %v", err)
	}
	select {
	case <-c.reconcileTrigger:
	default:
		t.Error("automatic reconciliation was not queued")
	}
	select {
	case <-c.observeTrigger:
	default:
		t.Error("manual-mode observation was not queued")
	}
	select {
	case <-c.collectorTrigger:
	default:
		t.Error("Collector topology and ingress reconciliation was not queued")
	}
}
