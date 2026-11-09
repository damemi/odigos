package http

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/odigos-io/odigos-enterprise/insights/internal/engine"
)

// errBadRequest marks a service-layer validation failure the caller can fix;
// wrap it (fmt.Errorf("%w: ...", errBadRequest)) so writeAPIError answers 400.
var errBadRequest = errors.New("bad request")

// writeAPIError maps shared service-layer errors onto HTTP status codes.
func writeAPIError(c *gin.Context, err error) {
	var partial *bulkPartialError
	switch {
	case errors.Is(err, errAdminUnavailable), errors.Is(err, errSystemSettingsUnavailable):
		writeError(c, http.StatusServiceUnavailable, err)
	case errors.Is(err, errTransactionNotFound), errors.Is(err, engine.ErrRecommendationNotFound):
		writeError(c, http.StatusNotFound, err)
	case errors.Is(err, errBadRequest):
		writeError(c, http.StatusBadRequest, err)
	case errors.As(err, &partial):
		writeError(c, http.StatusUnprocessableEntity, err)
	default:
		writeError(c, http.StatusInternalServerError, err)
	}
}
