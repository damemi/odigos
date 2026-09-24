package http

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/odigos-io/odigos-enterprise/insights/internal/api/http/apimap"
)

// blastRadiusDefaultDepth mirrors the debug UI's serviceMapDepth: how many hops
// out the subgraph expands in each direction when the caller omits ?depth.
const blastRadiusDefaultDepth = 2

// blastRadiusMaxDepth bounds the requested depth; the store also caps the total
// node count, but a small depth ceiling keeps a single query from fanning out.
const blastRadiusMaxDepth = 5

// listServices is the GET /api/v1/services handler; it returns per-(namespace,
// service) rollups, most active first, for the group-by-service overview.
func (a *API) listServices(c *gin.Context) {
	services, err := a.Repo.ListServiceStats(c.Request.Context(), 1000)
	if err != nil {
		writeError(c, http.StatusInternalServerError, err)
		return
	}
	limit := a.LiveSystemSettings().MaxTransactionsPerService
	c.JSON(http.StatusOK, apimap.ServiceStatList(services, limit))
}

// listServiceNames is the GET /api/v1/service-names handler; it returns every
// distinct service name Insights has seen, for guardrail caller/callee pickers.
func (a *API) listServiceNames(c *gin.Context) {
	names, err := a.Repo.ListServiceNames(c.Request.Context())
	if err != nil {
		writeError(c, http.StatusInternalServerError, err)
		return
	}
	c.JSON(http.StatusOK, apimap.ServiceNameList(names))
}

// getServiceProfile is the GET /api/v1/services/profile handler; it returns
// observed callers/callees/egress/transactions for one service so Guardrails
// can seed allowlists from live traffic.
func (a *API) getServiceProfile(c *gin.Context) {
	service := strings.TrimSpace(c.Query("service"))
	if service == "" {
		writeError(c, http.StatusBadRequest, errors.New("missing service"))
		return
	}
	namespace := strings.TrimSpace(c.Query("namespace"))

	profile, err := a.Repo.GetServiceProfile(c.Request.Context(), namespace, service)
	if err != nil {
		writeError(c, http.StatusInternalServerError, err)
		return
	}
	c.JSON(http.StatusOK, apimap.FromServiceProfile(profile))
}

// getServiceBlastRadius is the GET /api/v1/services/blast-radius handler; it
// returns the service-graph neighborhood (callers/callees plus virtual
// dependencies) around one service for the per-anomaly blast-radius map.
func (a *API) getServiceBlastRadius(c *gin.Context) {
	service := strings.TrimSpace(c.Query("service"))
	if service == "" {
		writeError(c, http.StatusBadRequest, errors.New("missing service"))
		return
	}
	namespace := strings.TrimSpace(c.Query("namespace"))

	depth := blastRadiusDefaultDepth
	if raw := strings.TrimSpace(c.Query("depth")); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 {
			writeError(c, http.StatusBadRequest, errors.New("depth must be a positive integer"))
			return
		}
		if n > blastRadiusMaxDepth {
			n = blastRadiusMaxDepth
		}
		depth = n
	}

	sub, err := a.Repo.QueryBlastRadius(c.Request.Context(), namespace, service, depth)
	if err != nil {
		writeError(c, http.StatusInternalServerError, err)
		return
	}
	c.JSON(http.StatusOK, apimap.FromBlastRadiusSubgraph(sub))
}
