package clickhouse

import (
	"context"
	"fmt"
	"net"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"

	"github.com/odigos-io/odigos/insights/internal/repo"
)

// maxBlastRadiusNodes caps a subgraph so a highly-connected service (e.g. a
// shared gateway) can't expand into the whole cluster and blow the response up.
const maxBlastRadiusNodes = 200

// UpsertServiceGraphEdges appends one row per edge to service_graph_edges. The
// ReplacingMergeTree(observed_at) collapses repeats for the same (edge, pod) to
// the newest observed_at, so re-sending an edge is idempotent.
func (r *Repo) UpsertServiceGraphEdges(ctx context.Context, edges []repo.ServiceGraphEdge) error {
	if len(edges) == 0 {
		return nil
	}
	err := r.insert(ctx,
		`INSERT INTO service_graph_edges
			(client_namespace, client_service, server_namespace, server_service,
			 connection_type, server_is_virtual, client_is_virtual, collector_pod, request_count, failed_count, observed_at)`,
		func(b driver.Batch) error {
			for _, e := range edges {
				observedAt := e.ObservedAt
				if observedAt.IsZero() {
					observedAt = time.Now()
				}
				var serverVirtual, clientVirtual uint8
				if e.ServerIsVirtual {
					serverVirtual = 1
				}
				if e.ClientIsVirtual {
					clientVirtual = 1
				}
				if err := b.Append(
					e.ClientNamespace, e.ClientService, e.ServerNamespace, e.ServerService,
					e.ConnectionType, serverVirtual, clientVirtual, e.CollectorPod,
					e.RequestCount, e.FailedCount, observedAt,
				); err != nil {
					return err
				}
			}
			return nil
		})
	if err != nil {
		return fmt.Errorf("upsert service graph edges: %w", err)
	}
	return nil
}

// serviceGraphEdgeCols aggregates the per-(edge, pod) rows into one logical edge:
// FINAL collapses the ReplacingMergeTree to the latest datapoint per full key,
// then GROUP BY the direction 5-tuple sums the counts across gateway pods and
// keeps the most recent observed_at as last_seen. *_is_virtual is max'd
// (any pod that saw it as virtual wins). At blast-radius scale the neighborhood
// filter keeps the FINAL read narrow.
const serviceGraphEdgeCols = `client_namespace, client_service, server_namespace, server_service, connection_type,
	max(server_is_virtual) AS server_is_virtual,
	max(client_is_virtual) AS client_is_virtual,
	sum(request_count)     AS request_count,
	sum(failed_count)      AS failed_count,
	max(observed_at)       AS last_seen`

const serviceGraphEdgeGroupBy = `GROUP BY client_namespace, client_service, server_namespace, server_service, connection_type`

// sgEdgeRow is one aggregated edge plus each endpoint's virtual flag (which
// belongs to the node, not the edge, so it isn't part of repo.BlastRadiusEdge).
type sgEdgeRow struct {
	edge            repo.BlastRadiusEdge
	serverIsVirtual bool
	clientIsVirtual bool
}

func scanServiceGraphEdge(s rowScanner) (sgEdgeRow, error) {
	var (
		row                          sgEdgeRow
		serverVirtual, clientVirtual uint8
	)
	if err := s.Scan(
		&row.edge.ClientNamespace, &row.edge.ClientService,
		&row.edge.ServerNamespace, &row.edge.ServerService, &row.edge.ConnectionType,
		&serverVirtual, &clientVirtual, &row.edge.RequestCount, &row.edge.FailedCount, &row.edge.LastSeen,
	); err != nil {
		return sgEdgeRow{}, err
	}
	row.serverIsVirtual = serverVirtual != 0
	row.clientIsVirtual = clientVirtual != 0 || syntheticVirtualClient(row.edge)
	return row, nil
}

// syntheticVirtualClient is the connector's invented ingress client ("user",
// empty namespace) for SERVER spans that never paired with a CLIENT span.
// Rows written before client_is_virtual was stored still match this identity.
func syntheticVirtualClient(e repo.BlastRadiusEdge) bool {
	return e.ClientNamespace == "" && e.ClientService == "user"
}

// virtualIPNode reports whether an endpoint is a virtual node named by a raw IP
// address (with or without a port). These appear when an instrumented client
// talks to an uninstrumented peer by address - most notably a metrics scraper
// hitting pods by IP, which spawns a distinct virtual node per target and
// floods the blast-radius view. The edges stay in storage; we just skip them
// when building the returned subgraph. A meaningfully-named virtual dependency
// (a DB, queue, or hostname) is unaffected.
func virtualIPNode(service string, virtual bool) bool {
	if !virtual {
		return false
	}
	if net.ParseIP(service) != nil {
		return true
	}
	host, _, err := net.SplitHostPort(service)
	return err == nil && net.ParseIP(host) != nil
}

// edgesTouching returns every logical edge with (namespace, service) on either
// end - the inbound (callers) and outbound (callees) of one node.
func (r *Repo) edgesTouching(ctx context.Context, namespace, service string) ([]sgEdgeRow, error) {
	return queryRows(ctx, r.conn, "service graph edges touching",
		`SELECT `+serviceGraphEdgeCols+`
		 FROM service_graph_edges FINAL
		 WHERE (server_namespace = ? AND server_service = ?)
		    OR (client_namespace = ? AND client_service = ?)
		 `+serviceGraphEdgeGroupBy,
		scanServiceGraphEdge, namespace, service, namespace, service)
}

// nodeKey identifies a graph node by its (namespace, service) identity.
type nodeKey struct{ ns, svc string }

// QueryBlastRadius does a breadth-first expansion from the anchor, following
// edges in both directions up to depth hops. Virtual (uninstrumented) nodes -
// both servers (DBs, external APIs) and clients (the synthetic "user" caller)
// - are treated as leaves so they end the walk instead of acting as a hub.
func (r *Repo) QueryBlastRadius(ctx context.Context, namespace, service string, depth int) (repo.BlastRadiusSubgraph, error) {
	return expandBlastRadius(ctx, namespace, service, depth, r.edgesTouching)
}

func expandBlastRadius(ctx context.Context, namespace, service string, depth int, touching func(context.Context, string, string) ([]sgEdgeRow, error)) (repo.BlastRadiusSubgraph, error) {
	if depth <= 0 {
		depth = 1
	}
	root := repo.BlastRadiusNode{Namespace: namespace, Service: service}

	nodes := map[nodeKey]repo.BlastRadiusNode{{namespace, service}: root}
	seenEdge := map[string]struct{}{}
	var edges []repo.BlastRadiusEdge

	frontier := []nodeKey{{namespace, service}}
	for hop := 0; hop < depth && len(frontier) > 0 && len(nodes) < maxBlastRadiusNodes; hop++ {
		var next []nodeKey
		for _, cur := range frontier {
			rows, err := touching(ctx, cur.ns, cur.svc)
			if err != nil {
				return repo.BlastRadiusSubgraph{}, err
			}
			for _, row := range rows {
				// Drop edges to/from raw-IP virtual nodes (e.g. a metrics
				// scraper hitting pods by IP) so they neither clutter the
				// subgraph nor consume the node cap. Still persisted upstream.
				if virtualIPNode(row.edge.ClientService, row.clientIsVirtual) ||
					virtualIPNode(row.edge.ServerService, row.serverIsVirtual) {
					continue
				}
				id := edgeID(row.edge)
				if _, ok := seenEdge[id]; !ok {
					seenEdge[id] = struct{}{}
					edges = append(edges, row.edge)
				}
				clientNode := nodeKey{row.edge.ClientNamespace, row.edge.ClientService}
				serverNode := nodeKey{row.edge.ServerNamespace, row.edge.ServerService}

				addNode(nodes, &next, clientNode, row.clientIsVirtual)
				addNode(nodes, &next, serverNode, row.serverIsVirtual)
			}
		}
		frontier = next
	}

	out := repo.BlastRadiusSubgraph{Root: root, Edges: edges}
	for _, n := range nodes {
		out.Nodes = append(out.Nodes, n)
	}
	return out, nil
}

// addNode records a newly-discovered node and, unless it is a virtual leaf,
// queues it for the next BFS hop. Already-known nodes are left untouched so the
// anchor keeps its identity and virtual endpoints don't get re-queried.
func addNode(nodes map[nodeKey]repo.BlastRadiusNode, next *[]nodeKey, k nodeKey, virtual bool) {
	if _, ok := nodes[k]; ok {
		return
	}
	nodes[k] = repo.BlastRadiusNode{Namespace: k.ns, Service: k.svc, IsVirtual: virtual}
	if !virtual {
		*next = append(*next, k)
	}
}

// edgeID is the dedup key for an edge across BFS hops (both endpoints plus the
// connection type, matching the aggregation GROUP BY).
func edgeID(e repo.BlastRadiusEdge) string {
	return e.ClientNamespace + "\x00" + e.ClientService + "\x00" +
		e.ServerNamespace + "\x00" + e.ServerService + "\x00" + e.ConnectionType
}
