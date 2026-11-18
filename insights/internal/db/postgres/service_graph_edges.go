package postgres

import (
	"context"
	"fmt"
	"net"
	"sort"
	"time"

	"github.com/odigos-io/odigos/insights/internal/repo"
)

// maxBlastRadiusNodes caps a subgraph so a highly-connected service (e.g. a
// shared gateway) can't expand into the whole cluster and blow the response up.
const maxBlastRadiusNodes = 200

// UpsertServiceGraphEdges stores the latest cumulative counts per (edge,
// collector pod). An older datapoint never overwrites a newer one, so
// re-sending an edge is idempotent.
func (r *Repo) UpsertServiceGraphEdges(ctx context.Context, edges []repo.ServiceGraphEdge) error {
	if len(edges) == 0 {
		return nil
	}
	sorted := append([]repo.ServiceGraphEdge(nil), edges...)
	sort.Slice(sorted, func(i, j int) bool { return serviceGraphEdgeKey(sorted[i]) < serviceGraphEdgeKey(sorted[j]) })
	now := time.Now()
	rows := make([][]any, 0, len(sorted))
	for _, e := range sorted {
		observedAt := e.ObservedAt
		if observedAt.IsZero() {
			observedAt = now
		}
		rows = append(rows, []any{
			e.ServerNamespace, e.ServerService, e.ClientNamespace, e.ClientService, e.ConnectionType, e.CollectorPod,
			e.ServerIsVirtual, e.ClientIsVirtual, int64(e.RequestCount), int64(e.FailedCount), observedAt,
		})
	}
	err := execBatch(ctx, r.pool,
		`INSERT INTO service_graph_edges AS e
			(server_namespace, server_service, client_namespace, client_service, connection_type, collector_pod,
			 server_is_virtual, client_is_virtual, request_count, failed_count, observed_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
		 ON CONFLICT (server_namespace, server_service, client_namespace, client_service, connection_type, collector_pod) DO UPDATE SET
			server_is_virtual = EXCLUDED.server_is_virtual, client_is_virtual = EXCLUDED.client_is_virtual,
			request_count = EXCLUDED.request_count, failed_count = EXCLUDED.failed_count, observed_at = EXCLUDED.observed_at
		 WHERE EXCLUDED.observed_at >= e.observed_at`,
		rows)
	if err != nil {
		return fmt.Errorf("upsert service graph edges: %w", err)
	}
	return nil
}

func serviceGraphEdgeKey(e repo.ServiceGraphEdge) string {
	return e.ServerNamespace + "\x00" + e.ServerService + "\x00" + e.ClientNamespace + "\x00" +
		e.ClientService + "\x00" + e.ConnectionType + "\x00" + e.CollectorPod
}

// serviceGraphEdgeCols aggregates the per-(edge, pod) rows into one logical
// edge: counts are summed across gateway pods and the newest observed_at is the
// edge's last_seen. *_is_virtual is OR'd (any pod that saw it as virtual wins).
const serviceGraphEdgeCols = `client_namespace, client_service, server_namespace, server_service, connection_type,
	bool_or(server_is_virtual) AS server_is_virtual,
	bool_or(client_is_virtual) AS client_is_virtual,
	sum(request_count)::bigint AS request_count,
	sum(failed_count)::bigint  AS failed_count,
	max(observed_at)           AS last_seen`

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
		row                     sgEdgeRow
		clientVirtual           bool
		requestCount, failedCnt int64
	)
	if err := s.Scan(
		&row.edge.ClientNamespace, &row.edge.ClientService,
		&row.edge.ServerNamespace, &row.edge.ServerService, &row.edge.ConnectionType,
		&row.serverIsVirtual, &clientVirtual, &requestCount, &failedCnt, &row.edge.LastSeen,
	); err != nil {
		return sgEdgeRow{}, err
	}
	row.edge.RequestCount, row.edge.FailedCount = uint64(requestCount), uint64(failedCnt)
	row.clientIsVirtual = clientVirtual || syntheticVirtualClient(row.edge)
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
	return queryRows(ctx, r.pool, "service graph edges touching",
		`SELECT `+serviceGraphEdgeCols+`
		 FROM service_graph_edges
		 WHERE (server_namespace = $1 AND server_service = $2)
		    OR (client_namespace = $1 AND client_service = $2)
		 `+serviceGraphEdgeGroupBy,
		scanServiceGraphEdge, namespace, service)
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
