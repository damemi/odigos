package http

import (
	"context"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/odigos-io/odigos-enterprise/insights/internal/api/http/apimap"
	"github.com/odigos-io/odigos-enterprise/insights/internal/api/http/apitypes"
	"github.com/odigos-io/odigos-enterprise/insights/internal/metrics"
	"github.com/odigos-io/odigos-enterprise/insights/internal/repo"
)

// LoadStorageHealth is the shared read path for GET /api/v1/storage-health
// and the debug Storage page.
func (a *API) LoadStorageHealth(ctx context.Context) (apitypes.StorageHealth, error) {
	h, err := a.Repo.GetStorageHealth(ctx)
	if err != nil {
		return apitypes.StorageHealth{}, err
	}
	h.Writeback = storageWritebackSnapshot(a)
	return apimap.FromStorageHealth(h), nil
}

func storageWritebackSnapshot(a *API) repo.StorageWriteback {
	wb := repo.StorageWriteback{FlushIntervalSeconds: repo.DefaultFlushIntervalSeconds}
	if s := a.LiveSystemSettings(); s.FlushIntervalSeconds > 0 {
		wb.FlushIntervalSeconds = s.FlushIntervalSeconds
	}
	ok, at, errMsg, ever := metrics.LastBaselineFlush()
	wb.EverFlushed = ever
	wb.LastFlushOK = ok
	wb.LastFlushAt = at
	wb.LastFlushError = errMsg
	return wb
}

func (a *API) getStorageHealth(c *gin.Context) {
	h, err := a.LoadStorageHealth(c.Request.Context())
	if err != nil {
		writeError(c, http.StatusInternalServerError, err)
		return
	}
	c.JSON(http.StatusOK, h)
}
