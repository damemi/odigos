package debugui

import (
	"fmt"
	"math"
	"sort"
	"strconv"

	"github.com/odigos-io/odigos-enterprise/insights/internal/repo"
)

// Service-map view geometry, in SVG user units (== CSS px). The layout is a
// simple layered graph: callers to the left of the anchor, callees to the
// right, one column per hop distance, nodes stacked and vertically centered
// within their column. The map is embedded per service on the Findings page.
const (
	// serviceMapDepth is how many hops out the per-service Findings map expands.
	serviceMapDepth = 2

	brColStep   = 210 // horizontal gap between hop columns
	brRowStep   = 66  // vertical gap between nodes sharing a column
	brMarginX   = 40
	brMarginY   = 40
	brNodeHalfW = 78 // node box half-width
	brNodeHalfH = 18 // node box half-height
	brMinWidth  = 560
	brMinHeight = 200

	// brNodeCapHint mirrors clickhouse.maxBlastRadiusNodes; when the subgraph
	// comes back at (or above) the cap we tell the user it was truncated. Kept
	// as a local constant to avoid importing the storage package.
	brNodeCapHint = 200

	// brLabelMax caps a rendered node label; the full "namespace/service" is
	// always available via the node's SVG <title> on hover.
	brLabelMax = 20
)

// brNode is one laid-out node: its center plus the pre-computed rect and text
// coordinates so the template does no arithmetic.
type brNode struct {
	Service     string
	Namespace   string
	DisplayName string
	Title       string // "namespace/service", shown on hover
	CX, CY      int
	RX, RY      int
	RW, RH      int
	NameY       int
	NsY         int
	IsRoot      bool
	IsVirtual   bool
	ShowNs      bool
}

// brEdge is one laid-out directed edge, clipped to the two node borders so the
// arrowhead lands on the target box rather than under it. Traffic counts are not
// drawn on the edge; they live in the hover Title to keep the graph uncluttered.
type brEdge struct {
	X1, Y1      int
	X2, Y2      int
	HasFailures bool
	Title       string
}

// serviceMapView is a laid-out blast-radius subgraph ready for SVG rendering,
// embedded in a service's card on the Findings page. HasGraph is false when the
// anchor has no recorded edges yet, so the template can show a short note
// instead of an empty canvas.
type serviceMapView struct {
	Depth     int
	HasGraph  bool
	Width     int
	Height    int
	Nodes     []brNode
	Edges     []brEdge
	NodeCount int
	EdgeCount int
	Truncated bool
}

// buildServiceMap turns a subgraph into a laid-out SVG view model. Columns are
// assigned by directional BFS from the root - callees to the right (positive),
// callers to the left (negative) - preferring the shorter relationship when a
// node is reachable both ways, with an undirected fallback for mixed paths.
func buildServiceMap(sub repo.BlastRadiusSubgraph) serviceMapView {
	var v serviceMapView
	rootKey := repo.ServiceScopeKey(sub.Root.Namespace, sub.Root.Service)

	type nodeInfo struct {
		n   repo.BlastRadiusNode
		col int
		set bool
	}
	nodes := make(map[string]*nodeInfo, len(sub.Nodes)+1)
	for _, n := range sub.Nodes {
		nodes[repo.ServiceScopeKey(n.Namespace, n.Service)] = &nodeInfo{n: n}
	}
	if _, ok := nodes[rootKey]; !ok {
		nodes[rootKey] = &nodeInfo{n: sub.Root}
	}

	forward := map[string][]string{}
	reverse := map[string][]string{}
	undirected := map[string][]string{}
	for _, e := range sub.Edges {
		ck := repo.ServiceScopeKey(e.ClientNamespace, e.ClientService)
		sk := repo.ServiceScopeKey(e.ServerNamespace, e.ServerService)
		forward[ck] = append(forward[ck], sk)
		reverse[sk] = append(reverse[sk], ck)
		undirected[ck] = append(undirected[ck], sk)
		undirected[sk] = append(undirected[sk], ck)
	}

	root := nodes[rootKey]
	root.col, root.set = 0, true
	setCol := func(k string, col int) {
		ni, ok := nodes[k]
		if !ok {
			return
		}
		if !ni.set || absInt(col) < absInt(ni.col) {
			ni.col, ni.set = col, true
		}
	}
	// Callees (right, +), then callers (left, -), then an undirected fallback
	// (right, +) for nodes only reachable via a mixed-direction path.
	bfs(forward, rootKey, func(k string, hop int) { setCol(k, hop) })
	bfs(reverse, rootKey, func(k string, hop int) { setCol(k, -hop) })
	bfs(undirected, rootKey, func(k string, hop int) { setCol(k, hop) })
	for _, ni := range nodes {
		if !ni.set {
			ni.col, ni.set = 0, true
		}
	}

	cols := map[int][]*nodeInfo{}
	for _, ni := range nodes {
		cols[ni.col] = append(cols[ni.col], ni)
	}
	colKeys := make([]int, 0, len(cols))
	for col := range cols {
		colKeys = append(colKeys, col)
	}
	sort.Ints(colKeys)
	maxRows := 0
	for _, col := range colKeys {
		list := cols[col]
		sort.Slice(list, func(i, j int) bool {
			if list[i].n.Service != list[j].n.Service {
				return list[i].n.Service < list[j].n.Service
			}
			return list[i].n.Namespace < list[j].n.Namespace
		})
		if len(list) > maxRows {
			maxRows = len(list)
		}
	}

	width := 2*brMarginX + 2*brNodeHalfW + (len(colKeys)-1)*brColStep
	if width < brMinWidth {
		width = brMinWidth
	}
	height := 2*brMarginY + 2*brNodeHalfH + (maxRows-1)*brRowStep
	if height < brMinHeight {
		height = brMinHeight
	}
	centerY := height / 2

	pos := map[string][2]int{}
	for i, col := range colKeys {
		list := cols[col]
		x := brMarginX + brNodeHalfW + i*brColStep
		startY := centerY - (len(list)-1)*brRowStep/2
		for j, ni := range list {
			y := startY + j*brRowStep
			key := repo.ServiceScopeKey(ni.n.Namespace, ni.n.Service)
			pos[key] = [2]int{x, y}
			showNs := ni.n.Namespace != ""
			nameY, nsY := y+4, y+12
			if showNs {
				nameY = y - 3
			}
			v.Nodes = append(v.Nodes, brNode{
				Service:     ni.n.Service,
				Namespace:   ni.n.Namespace,
				DisplayName: truncateLabel(ni.n.Service),
				Title:       key,
				CX:          x,
				CY:          y,
				RX:          x - brNodeHalfW,
				RY:          y - brNodeHalfH,
				RW:          2 * brNodeHalfW,
				RH:          2 * brNodeHalfH,
				NameY:       nameY,
				NsY:         nsY,
				IsRoot:      key == rootKey,
				IsVirtual:   ni.n.IsVirtual,
				ShowNs:      showNs,
			})
		}
	}

	for _, e := range sub.Edges {
		cp, ok1 := pos[repo.ServiceScopeKey(e.ClientNamespace, e.ClientService)]
		sp, ok2 := pos[repo.ServiceScopeKey(e.ServerNamespace, e.ServerService)]
		if !ok1 || !ok2 {
			continue
		}
		x1, y1 := borderPoint(cp[0], cp[1], sp[0], sp[1])
		x2, y2 := borderPoint(sp[0], sp[1], cp[0], cp[1])
		v.Edges = append(v.Edges, brEdge{
			X1: x1, Y1: y1, X2: x2, Y2: y2,
			HasFailures: e.FailedCount > 0,
			Title: fmt.Sprintf("%s → %s (%s) · %s",
				e.ClientService, e.ServerService, e.ConnectionType, edgeLabel(e)),
		})
	}

	v.Width = width
	v.Height = height
	v.NodeCount = len(v.Nodes)
	v.EdgeCount = len(v.Edges)
	v.HasGraph = len(v.Nodes) > 1 || len(v.Edges) > 0
	v.Truncated = len(sub.Nodes) >= brNodeCapHint
	return v
}

// bfs walks adj breadth-first from start, calling visit once per node with its
// hop distance (start included at hop 0).
func bfs(adj map[string][]string, start string, visit func(k string, hop int)) {
	seen := map[string]bool{start: true}
	type qn struct {
		k   string
		hop int
	}
	queue := []qn{{start, 0}}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		visit(cur.k, cur.hop)
		for _, nb := range adj[cur.k] {
			if !seen[nb] {
				seen[nb] = true
				queue = append(queue, qn{nb, cur.hop + 1})
			}
		}
	}
}

// borderPoint returns the point on the border of the node box centered at
// (cx, cy) in the direction of (tx, ty), so an edge starts/ends flush with the
// box instead of at its center.
func borderPoint(cx, cy, tx, ty int) (int, int) {
	dx, dy := float64(tx-cx), float64(ty-cy)
	if dx == 0 && dy == 0 {
		return cx, cy
	}
	tX, tY := math.Inf(1), math.Inf(1)
	if dx != 0 {
		tX = float64(brNodeHalfW) / math.Abs(dx)
	}
	if dy != 0 {
		tY = float64(brNodeHalfH) / math.Abs(dy)
	}
	t := math.Min(tX, tY)
	return cx + int(dx*t), cy + int(dy*t)
}

// edgeLabel renders an edge's traffic for the hover tooltip: request count, plus
// error count when the connector saw failures on that edge.
func edgeLabel(e repo.BlastRadiusEdge) string {
	if e.FailedCount > 0 {
		return fmt.Sprintf("%s req · %s err", humanNum(e.RequestCount), humanNum(e.FailedCount))
	}
	return humanNum(e.RequestCount) + " req"
}

// humanNum renders a uint64 with a k/M suffix so busy edges stay readable.
func humanNum(n uint64) string {
	switch {
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1e6)
	case n >= 1_000:
		return fmt.Sprintf("%.1fk", float64(n)/1e3)
	default:
		return strconv.FormatUint(n, 10)
	}
}

// truncateLabel shortens a node label to brLabelMax runes with an ellipsis.
func truncateLabel(s string) string {
	r := []rune(s)
	if len(r) <= brLabelMax {
		return s
	}
	return string(r[:brLabelMax-1]) + "…"
}

func absInt(n int) int {
	if n < 0 {
		return -n
	}
	return n
}
