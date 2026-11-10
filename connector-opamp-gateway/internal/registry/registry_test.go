package registry

import (
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/cache"

	cloudconnectorsv1 "github.com/odigos-io/odigos/api/cloud-connectors/v1alpha1"

	"github.com/odigos-io/odigos/connectors/base/domain/collectorgateway"
)

const uid = "uid-1"

func connectorCR(mutate func(*cloudconnectorsv1.OdigosCloudConnector)) *cloudconnectorsv1.OdigosCloudConnector {
	cr := &cloudconnectorsv1.OdigosCloudConnector{
		ObjectMeta: metav1.ObjectMeta{Name: "aws-prod", Namespace: "odigos-system", UID: types.UID(uid)},
		Spec: cloudconnectorsv1.OdigosCloudConnectorSpec{
			Provider: "aws",
			Account:  cloudconnectorsv1.CloudConnectorAccount{ID: "123456789012"},
		},
	}
	if mutate != nil {
		mutate(cr)
	}
	return cr
}

func connectorService(ports []corev1.ServicePort, labels map[string]string) *corev1.Service {
	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "connector-aws-prod", Namespace: "odigos-system", Labels: labels},
		Spec:       corev1.ServiceSpec{Ports: ports},
	}
}

func routableLabels() map[string]string {
	return map[string]string{collectorgateway.LabelConnectorID: uid}
}

// TestConnectorRegistration covers what a credential is authorized against.
func TestConnectorRegistration(t *testing.T) {
	r := New()

	if _, ok := r.ByUID(uid); ok {
		t.Error("an empty registry resolved a connector")
	}

	r.upsertConnector(connectorCR(nil))
	got, ok := r.ByUID(uid)
	if !ok {
		t.Fatal("the connector did not register")
	}
	if got.Provider != "aws" || got.AccountID != "123456789012" || got.Name != "aws-prod" {
		t.Errorf("registration = %+v", got)
	}
	if got.Terminating {
		t.Error("a live connector registered as terminating")
	}

	// A deletion timestamp, not a phase: teardown revokes credentials before deleting the connector, so
	// a connection arriving in this window is one the revoke has not reached yet.
	r.upsertConnector(connectorCR(func(cr *cloudconnectorsv1.OdigosCloudConnector) {
		ts := metav1.Now()
		cr.DeletionTimestamp = &ts
		cr.Finalizers = []string{"odigos.io/connector-cleanup"}
	}))
	got, _ = r.ByUID(uid)
	if !got.Terminating {
		t.Error("a connector being deleted did not register as terminating")
	}

	r.deleteConnector(connectorCR(nil))
	if _, ok := r.ByUID(uid); ok {
		t.Error("a deleted connector is still registered")
	}
}

// TestByUIDReturnsACopy: handlers must not be able to edit registry state that every other connection
// reads.
func TestByUIDReturnsACopy(t *testing.T) {
	r := New()
	r.upsertConnector(connectorCR(nil))

	first, _ := r.ByUID(uid)
	first.AccountID = "999999999999"
	first.Terminating = true

	second, _ := r.ByUID(uid)
	if second.AccountID != "123456789012" || second.Terminating {
		t.Errorf("a caller mutated shared registry state: %+v", second)
	}
}

// TestUpstreamResolution: the route comes from the Service the connector actually has.
func TestUpstreamResolution(t *testing.T) {
	r := New()
	r.upsertConnector(connectorCR(nil))
	conn, _ := r.ByUID(uid)

	// Before any Service exists there is nowhere to route, and the gateway must say so rather than
	// synthesize an address. A guessed name resolves, connects to nothing, and reports healthy.
	if _, err := r.Upstream(conn); err == nil {
		t.Error("Upstream invented an address for a connector with no Service")
	} else if !strings.Contains(err.Error(), collectorgateway.LabelConnectorID) {
		t.Errorf("the error does not say what is missing: %v", err)
	}

	r.upsertService(connectorService([]corev1.ServicePort{
		{Name: "graphql", Port: 8080},
		{Name: collectorgateway.ServicePortName, Port: 4320},
	}, routableLabels()))

	u, err := r.Upstream(conn)
	if err != nil {
		t.Fatalf("Upstream: %v", err)
	}
	if u.Scheme != "http" || u.Host != "connector-aws-prod.odigos-system.svc:4320" {
		t.Errorf("upstream = %s", u)
	}

	r.deleteService(connectorService(nil, routableLabels()))
	if _, err := r.Upstream(conn); err == nil {
		t.Error("a deleted Service is still routed to")
	}
}

// TestOpAMPPortSelection: by name first, because a Service carries several ports and their order is not
// a contract. Picking the wrong one routes OpAMP at the connector's GraphQL server.
func TestOpAMPPortSelection(t *testing.T) {
	cases := []struct {
		name  string
		ports []corev1.ServicePort
		want  int32
		found bool
	}{
		{
			name: "named port wins over position",
			ports: []corev1.ServicePort{
				{Name: "graphql", Port: 8080},
				{Name: "odictl", Port: 23678},
				{Name: collectorgateway.ServicePortName, Port: 4320},
			},
			want: 4320, found: true,
		},
		{
			name:  "a named port on a non-default number is honored",
			ports: []corev1.ServicePort{{Name: collectorgateway.ServicePortName, Port: 9999}},
			want:  9999, found: true,
		},
		{
			name:  "the well-known number is the fallback for an unnamed port",
			ports: []corev1.ServicePort{{Port: 8080}, {Port: collectorgateway.DefaultServerPort}},
			want:  collectorgateway.DefaultServerPort, found: true,
		},
		{
			// The state until connector-runtime exposes the port. Better to resolve nothing than to
			// route OpAMP at whatever the connector's first port happens to be.
			name:  "a Service with no OpAMP port resolves to nothing",
			ports: []corev1.ServicePort{{Name: "graphql", Port: 8080}},
		},
		{name: "a Service with no ports at all resolves to nothing"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := opampPort(connectorService(tc.ports, routableLabels()))
			if ok != tc.found {
				t.Fatalf("found = %v, want %v", ok, tc.found)
			}
			if got != tc.want {
				t.Errorf("port = %d, want %d", got, tc.want)
			}
		})
	}
}

// TestUnlabelledServiceIsIgnored: the gateway routes to connector Services, and a customer's Service
// that happens to expose 4320 must not become a route.
func TestUnlabelledServiceIsIgnored(t *testing.T) {
	r := New()
	r.upsertConnector(connectorCR(nil))
	conn, _ := r.ByUID(uid)

	r.upsertService(connectorService(
		[]corev1.ServicePort{{Name: collectorgateway.ServicePortName, Port: 4320}}, nil))

	if _, err := r.Upstream(conn); err == nil {
		t.Error("an unlabelled Service became a route")
	}
}

// TestServiceLosingItsOpAMPPortStopsBeingRouted: a port removed from a Service must drop the route, not
// leave a stale one that keeps sending Collectors at a closed port.
func TestServiceLosingItsOpAMPPortStopsBeingRouted(t *testing.T) {
	r := New()
	r.upsertConnector(connectorCR(nil))
	conn, _ := r.ByUID(uid)

	r.upsertService(connectorService(
		[]corev1.ServicePort{{Name: collectorgateway.ServicePortName, Port: 4320}}, routableLabels()))
	if _, err := r.Upstream(conn); err != nil {
		t.Fatalf("Upstream: %v", err)
	}

	r.upsertService(connectorService([]corev1.ServicePort{{Name: "graphql", Port: 8080}}, routableLabels()))
	if _, err := r.Upstream(conn); err == nil {
		t.Error("a Service that lost its OpAMP port is still routed to")
	}
}

// TestTombstonedDeleteIsHandled: when the watch missed a deletion, the event carries a tombstone rather
// than the object. Ignoring it would leave a deleted connector registered for ever, and credentials
// bound to it would keep routing at a Service that no longer exists.
func TestTombstonedDeleteIsHandled(t *testing.T) {
	r := New()
	r.upsertConnector(connectorCR(nil))
	r.upsertService(connectorService(
		[]corev1.ServicePort{{Name: collectorgateway.ServicePortName, Port: 4320}}, routableLabels()))

	r.deleteConnector(cache.DeletedFinalStateUnknown{Key: "odigos-system/aws-prod", Obj: connectorCR(nil)})
	if _, ok := r.ByUID(uid); ok {
		t.Error("a tombstoned connector delete was ignored")
	}

	r.deleteService(cache.DeletedFinalStateUnknown{
		Key: "odigos-system/connector-aws-prod",
		Obj: connectorService(nil, routableLabels()),
	})
	if _, err := r.Upstream(&collectorgateway.Connector{UID: uid, Name: "aws-prod"}); err == nil {
		t.Error("a tombstoned Service delete was ignored")
	}
}

// TestHasSyncedGatesEverything: until the watch has listed, every connector looks unregistered. The
// handler answers 503 on that, so the flag must not start true.
func TestHasSyncedStartsFalse(t *testing.T) {
	r := New()
	if r.HasSynced() {
		t.Error("a registry reports synced before its watch has listed")
	}
	r.MarkSynced()
	if !r.HasSynced() {
		t.Error("MarkSynced did not take effect")
	}
}

// TestUpstreamRejectsNoConnector: a nil connector reaching here is a caller bug, and returning a
// zero-value URL would proxy somewhere unpredictable.
func TestUpstreamRejectsNoConnector(t *testing.T) {
	if _, err := New().Upstream(nil); err == nil {
		t.Error("Upstream accepted a nil connector")
	}
}
