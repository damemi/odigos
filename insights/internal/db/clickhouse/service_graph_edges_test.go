package clickhouse

import (
	"context"
	"testing"

	"github.com/odigos-io/odigos/insights/internal/repo"
)

func TestExpandBlastRadius_VirtualClientIsLeaf(t *testing.T) {
	// Shared synthetic "user" caller into two unrelated services, plus a
	// virtual DB on the leak service. Depth 2 must not walk through user.
	byNode := map[nodeKey][]sgEdgeRow{
		{"nodejs-db-permissions-leak", "db-permissions-leak"}: {
			{
				edge: repo.BlastRadiusEdge{
					ClientService: "user", ServerNamespace: "nodejs-db-permissions-leak",
					ServerService: "db-permissions-leak", ConnectionType: "virtual_node",
				},
				clientIsVirtual: true,
			},
			{
				edge: repo.BlastRadiusEdge{
					ClientNamespace: "nodejs-db-permissions-leak", ClientService: "db-permissions-leak",
					ServerService: "permissions_leak", ConnectionType: "database",
				},
				serverIsVirtual: true,
			},
		},
		{"", "user"}: {
			{
				edge: repo.BlastRadiusEdge{
					ClientService: "user", ServerNamespace: "nodejs-db-permissions-leak",
					ServerService: "db-permissions-leak", ConnectionType: "virtual_node",
				},
				clientIsVirtual: true,
			},
			{
				edge: repo.BlastRadiusEdge{
					ClientService: "user", ServerNamespace: "default",
					ServerService: "frontend", ConnectionType: "virtual_node",
				},
				clientIsVirtual: true,
			},
			{
				edge: repo.BlastRadiusEdge{
					ClientService: "user", ServerNamespace: "default",
					ServerService: "geolocation", ConnectionType: "virtual_node",
				},
				clientIsVirtual: true,
			},
		},
		{"default", "frontend"}: {
			{
				edge: repo.BlastRadiusEdge{
					ClientService: "user", ServerNamespace: "default",
					ServerService: "frontend", ConnectionType: "virtual_node",
				},
				clientIsVirtual: true,
			},
			{
				edge: repo.BlastRadiusEdge{
					ClientNamespace: "default", ClientService: "frontend",
					ServerNamespace: "default", ServerService: "geolocation",
				},
			},
		},
	}
	touching := func(_ context.Context, ns, svc string) ([]sgEdgeRow, error) {
		return byNode[nodeKey{ns, svc}], nil
	}

	sub, err := expandBlastRadius(context.Background(), "nodejs-db-permissions-leak", "db-permissions-leak", 2, touching)
	if err != nil {
		t.Fatal(err)
	}

	got := map[string]bool{}
	for _, n := range sub.Nodes {
		got[n.Namespace+"/"+n.Service] = n.IsVirtual
	}
	if _, ok := got["default/frontend"]; ok {
		t.Errorf("frontend leaked through virtual user: %+v", got)
	}
	if _, ok := got["default/geolocation"]; ok {
		t.Errorf("geolocation leaked through virtual user: %+v", got)
	}
	if _, ok := got["nodejs-db-permissions-leak/db-permissions-leak"]; !ok {
		t.Errorf("missing root: %+v", got)
	}
	if virt, ok := got["/user"]; !ok || !virt {
		t.Errorf("expected virtual user leaf, got %+v", got)
	}
	if virt, ok := got["/permissions_leak"]; !ok || !virt {
		t.Errorf("expected virtual postgres leaf, got %+v", got)
	}
	if len(sub.Nodes) != 3 {
		t.Errorf("expected 3 nodes (leak, user, postgres), got %d: %+v", len(sub.Nodes), got)
	}
}

func TestExpandBlastRadius_SkipsVirtualIPNodes(t *testing.T) {
	// frontend calls a real backend, but also gets scraped by a metrics agent
	// that reaches it (and other pods) by raw IP. Those IP-named virtual nodes
	// must not appear in the subgraph, while the real callee stays.
	byNode := map[nodeKey][]sgEdgeRow{
		{"default", "frontend"}: {
			{
				edge: repo.BlastRadiusEdge{
					ClientNamespace: "default", ClientService: "frontend",
					ServerNamespace: "default", ServerService: "backend",
				},
			},
			{
				edge: repo.BlastRadiusEdge{
					ClientNamespace: "monitoring", ClientService: "alloy",
					ServerService: "10.244.1.7", ConnectionType: "virtual_node",
				},
				serverIsVirtual: true,
			},
			{
				edge: repo.BlastRadiusEdge{
					ClientNamespace: "monitoring", ClientService: "alloy",
					ServerService: "10.244.1.8:9090", ConnectionType: "virtual_node",
				},
				serverIsVirtual: true,
			},
		},
	}
	touching := func(_ context.Context, ns, svc string) ([]sgEdgeRow, error) {
		return byNode[nodeKey{ns, svc}], nil
	}

	sub, err := expandBlastRadius(context.Background(), "default", "frontend", 2, touching)
	if err != nil {
		t.Fatal(err)
	}

	got := map[string]bool{}
	for _, n := range sub.Nodes {
		got[n.Namespace+"/"+n.Service] = n.IsVirtual
	}
	if _, ok := got["/10.244.1.7"]; ok {
		t.Errorf("raw-IP virtual node leaked into subgraph: %+v", got)
	}
	if _, ok := got["/10.244.1.8:9090"]; ok {
		t.Errorf("raw-IP:port virtual node leaked into subgraph: %+v", got)
	}
	if _, ok := got["default/backend"]; !ok {
		t.Errorf("real callee should remain: %+v", got)
	}
	if len(sub.Nodes) != 2 {
		t.Errorf("expected 2 nodes (frontend, backend), got %d: %+v", len(sub.Nodes), got)
	}
}

func TestVirtualIPNode(t *testing.T) {
	cases := []struct {
		service string
		virtual bool
		want    bool
	}{
		{"10.244.1.7", true, true},
		{"10.244.1.8:9090", true, true},
		{"::1", true, true},
		{"[fe80::1]:80", true, true},
		{"10.244.1.7", false, false},
		{"postgres", true, false},
		{"postgres:5432", true, false},
		{"backend", false, false},
	}
	for _, tc := range cases {
		if got := virtualIPNode(tc.service, tc.virtual); got != tc.want {
			t.Errorf("virtualIPNode(%q, %v) = %v, want %v", tc.service, tc.virtual, got, tc.want)
		}
	}
}

func TestSyntheticVirtualClient(t *testing.T) {
	if !syntheticVirtualClient(repo.BlastRadiusEdge{ClientService: "user"}) {
		t.Error("empty-ns user should be treated as virtual")
	}
	if syntheticVirtualClient(repo.BlastRadiusEdge{ClientNamespace: "default", ClientService: "user"}) {
		t.Error("namespaced user is a real service name")
	}
	if syntheticVirtualClient(repo.BlastRadiusEdge{ClientService: "frontend"}) {
		t.Error("frontend is not the synthetic caller")
	}
}
