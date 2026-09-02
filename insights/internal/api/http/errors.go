package http

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"
)

// writeAPIError maps shared service-layer errors onto HTTP status codes.
func writeAPIError(c *gin.Context, err error) {
	var partial *bulkPartialError
	switch {
	case errors.Is(err, errAdminUnavailable), errors.Is(err, errSystemSettingsUnavailable):
		writeError(c, http.StatusServiceUnavailable, err)
	case errors.Is(err, errTransactionNotFound):
		writeError(c, http.StatusNotFound, err)
	case errors.As(err, &partial):
		writeError(c, http.StatusUnprocessableEntity, err)
	default:
		writeError(c, http.StatusInternalServerError, err)
	}
}
