package http

import (
	"net/http"

	"github.com/gin-gonic/gin"

	api "github.com/odigos-io/odigos-enterprise/insights/api"
)

// serveOpenAPISpec is the GET /api/v1/openapi.yaml handler; it returns the
// embedded OpenAPI contract verbatim so tools (Swagger UI, codegen, mock
// servers) and the frontend can fetch the single source of truth at runtime.
func serveOpenAPISpec(c *gin.Context) {
	c.Data(http.StatusOK, "application/yaml; charset=utf-8", api.OpenAPISpec)
}

// serveDocs is the GET /api/v1/docs handler; it serves a minimal Swagger UI page
// (assets from the jsDelivr CDN) that renders the spec fetched from
// /api/v1/openapi.yaml, giving the frontend team browsable, try-it-out docs.
func serveDocs(c *gin.Context) {
	c.Data(http.StatusOK, "text/html; charset=utf-8", swaggerUIHTML)
}

var swaggerUIHTML = []byte(`<!DOCTYPE html>
<html lang="en">
<head>
  <meta charset="UTF-8">
  <title>Odigos Insights API</title>
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <link rel="stylesheet" href="https://cdn.jsdelivr.net/npm/swagger-ui-dist@5/swagger-ui.css">
</head>
<body>
  <div id="swagger-ui"></div>
  <script src="https://cdn.jsdelivr.net/npm/swagger-ui-dist@5/swagger-ui-bundle.js" crossorigin></script>
  <script>
    window.onload = () => {
      window.ui = SwaggerUIBundle({
        url: 'openapi.yaml',
        dom_id: '#swagger-ui',
        deepLinking: true,
      });
    };
  </script>
</body>
</html>`)
