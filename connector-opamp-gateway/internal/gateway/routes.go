package gateway

import (
	"net/http"

	"github.com/odigos-io/odigos-enterprise/connectors/base/domain/collectorgateway"
)

// RegisterCollectorRoutes sends configuration and own-metrics traffic through the
// same credential authentication and collector identity boundary.
func RegisterCollectorRoutes(mux *http.ServeMux, opampPath string, handler http.Handler) {
	mux.Handle(opampPath, handler)
	mux.Handle("POST "+collectorgateway.MetricsPath, handler)
}
