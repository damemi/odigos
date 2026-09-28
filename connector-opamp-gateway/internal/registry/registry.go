// Package registry keeps the gateway's picture of which connectors exist and where their OpAMP servers
// listen, fed by watches rather than an API call per handshake.
//
// Two things are tracked per connector CR UID, from two sources, because they answer different
// questions and can be true at different times:
//
//   - the OdigosCloudConnector CR — does this connector exist, whose account does it own, is it being
//     deleted. This is what a credential is authorized against.
//   - its Service — where does its OpAMP server actually listen. Matched by the
//     collectorgateway.LabelConnectorID label, never by recomputing the connector's workload name.
package registry

import (
	"context"
	"fmt"
	"net/url"
	"sync"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/tools/cache"
	ctrlcache "sigs.k8s.io/controller-runtime/pkg/cache"

	cloudconnectorsv1 "github.com/odigos-io/odigos/api/cloud-connectors/v1alpha1"

	"github.com/odigos-io/odigos-enterprise/connectors/base/domain/collectorgateway"
)

// route is where one connector's OpAMP server listens.
type route struct {
	service   string
	namespace string
	port      int32
}

// Registry is the gateway's Connectors implementation. Safe for concurrent readers.
type Registry struct {
	mu         sync.RWMutex
	connectors map[string]collectorgateway.Connector // by CR UID
	routes     map[string]route                      // by CR UID

	syncedMu sync.RWMutex
	synced   bool
}

func New() *Registry {
	return &Registry{
		connectors: map[string]collectorgateway.Connector{},
		routes:     map[string]route{},
	}
}

// ByUID returns a COPY of a connector's registration. A pointer into the map would let a handler
// mutate state every other connection reads.
func (r *Registry) ByUID(uid string) (*collectorgateway.Connector, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	c, ok := r.connectors[uid]
	if !ok {
		return nil, false
	}
	return &c, true
}

// HasSynced reports whether both watches have completed their initial list.
func (r *Registry) HasSynced() bool {
	r.syncedMu.RLock()
	defer r.syncedMu.RUnlock()
	return r.synced
}

// Upstream resolves a connector's OpAMP base URL.
//
// Plain http: this hop is inside the cluster, between two pods, on a port a NetworkPolicy admits only
// the gateway to. Public TLS terminates at the edge. Introducing TLS here would mean issuing and
// rotating a certificate per connector for a hop the cluster already isolates.
func (r *Registry) Upstream(conn *collectorgateway.Connector) (*url.URL, error) {
	if conn == nil {
		return nil, fmt.Errorf("no connector to resolve")
	}
	r.mu.RLock()
	rt, ok := r.routes[conn.UID]
	r.mu.RUnlock()
	if !ok {
		// Expected until connector-runtime labels the connector Service and exposes the OpAMP port.
		// Reported rather than guessed: computing the Service name here would route to a name nothing
		// answers on and report success.
		return nil, fmt.Errorf("connector %q (uid %s) has no Service labelled %s=%s exposing its OpAMP port",
			conn.Name, conn.UID, collectorgateway.LabelConnectorID, conn.UID)
	}
	return &url.URL{
		Scheme: "http",
		Host:   fmt.Sprintf("%s.%s.svc:%d", rt.service, rt.namespace, rt.port),
	}, nil
}

// Start wires both watches into an already-configured controller-runtime cache and blocks until they
// have listed. The caller starts and stops the cache.
func (r *Registry) Start(ctx context.Context, c ctrlcache.Cache) error {
	connectorInformer, err := c.GetInformer(ctx, &cloudconnectorsv1.OdigosCloudConnector{})
	if err != nil {
		return fmt.Errorf("informer for OdigosCloudConnector: %w", err)
	}
	if _, err := connectorInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    func(obj any) { r.upsertConnector(obj) },
		UpdateFunc: func(_, obj any) { r.upsertConnector(obj) },
		DeleteFunc: func(obj any) { r.deleteConnector(obj) },
	}); err != nil {
		return fmt.Errorf("watch OdigosCloudConnector: %w", err)
	}

	serviceInformer, err := c.GetInformer(ctx, &corev1.Service{})
	if err != nil {
		return fmt.Errorf("informer for Service: %w", err)
	}
	if _, err := serviceInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    func(obj any) { r.upsertService(obj) },
		UpdateFunc: func(_, obj any) { r.upsertService(obj) },
		DeleteFunc: func(obj any) { r.deleteService(obj) },
	}); err != nil {
		return fmt.Errorf("watch Service: %w", err)
	}
	return nil
}

// MarkSynced is called once both caches have listed. Until then the handler answers 503 rather than
// reporting connectors as unknown — on a restart the whole fleet reconnects at once, and an empty
// cache would refuse all of it.
func (r *Registry) MarkSynced() {
	r.syncedMu.Lock()
	defer r.syncedMu.Unlock()
	r.synced = true
}

func (r *Registry) upsertConnector(obj any) {
	cr, ok := obj.(*cloudconnectorsv1.OdigosCloudConnector)
	if !ok || cr.UID == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.connectors[string(cr.UID)] = collectorgateway.Connector{
		UID:       string(cr.UID),
		Name:      cr.Name,
		Namespace: cr.Namespace,
		Provider:  cr.Spec.Provider,
		AccountID: cr.Spec.Account.ID,
		// A deletion timestamp, not a phase. Teardown revokes credentials before deleting the connector
		// (§6.4); a connection arriving in that window is one the revoke has not reached yet.
		Terminating: cr.DeletionTimestamp != nil,
	}
}

func (r *Registry) deleteConnector(obj any) {
	cr, ok := deref[*cloudconnectorsv1.OdigosCloudConnector](obj)
	if !ok || cr.UID == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.connectors, string(cr.UID))
}

func (r *Registry) upsertService(obj any) {
	svc, ok := obj.(*corev1.Service)
	if !ok {
		return
	}
	uid := svc.Labels[collectorgateway.LabelConnectorID]
	if uid == "" {
		return
	}
	port, ok := opampPort(svc)
	if !ok {
		// Labelled but not exposing OpAMP: drop any route we held, so a port removed from a Service
		// stops being routed to rather than lingering in the map.
		r.mu.Lock()
		defer r.mu.Unlock()
		delete(r.routes, uid)
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.routes[uid] = route{service: svc.Name, namespace: svc.Namespace, port: port}
}

func (r *Registry) deleteService(obj any) {
	svc, ok := deref[*corev1.Service](obj)
	if !ok {
		return
	}
	uid := svc.Labels[collectorgateway.LabelConnectorID]
	if uid == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.routes, uid)
}

// opampPort finds the OpAMP port by NAME, falling back to the well-known number.
//
// By name first because a Service may carry several ports and their order is not a contract; the
// number is the fallback for a Service that declares the port without naming it.
func opampPort(svc *corev1.Service) (int32, bool) {
	for _, p := range svc.Spec.Ports {
		if p.Name == collectorgateway.ServicePortName {
			return p.Port, true
		}
	}
	for _, p := range svc.Spec.Ports {
		if p.Port == collectorgateway.DefaultServerPort {
			return p.Port, true
		}
	}
	return 0, false
}

// deref unwraps the tombstone a delete event carries when the watch missed the deletion itself.
// Without it a connector deleted while the gateway was disconnected would stay registered for ever,
// and credentials bound to it would keep routing to a Service that no longer exists.
func deref[T any](obj any) (T, bool) {
	if t, ok := obj.(T); ok {
		return t, true
	}
	if tombstone, ok := obj.(cache.DeletedFinalStateUnknown); ok {
		if t, ok := tombstone.Obj.(T); ok {
			return t, true
		}
	}
	var zero T
	return zero, false
}
