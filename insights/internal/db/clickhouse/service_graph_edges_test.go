package clickhouse

import (
	"context"
	"testing"

	"github.com/odigos-io/odigos-enterprise/insights/internal/repo"
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
