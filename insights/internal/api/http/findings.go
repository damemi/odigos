package http

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// listFindings is the GET /api/v1/findings handler.
func (a *API) listFindings(c *gin.Context) {
	params, err := bindListFindingsParams(c)
	if err != nil {
		writeBindError(c, err)
		return
	}
	out, err := a.ListFindings(c.Request.Context(), params)
	if err != nil {
		writeError(c, http.StatusInternalServerError, err)
		return
	}
	c.JSON(http.StatusOK, out)
}
