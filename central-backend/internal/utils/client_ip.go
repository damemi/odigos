package utils

import (
	"context"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

type clientIPKey struct{}

// ForwardedClientIP returns the caller address a reverse proxy recorded in
// X-Forwarded-For, or "" when there is none. Central UI's /graphql rewrite
// forwards the header unchanged and adds nothing, so without a proxy in front
// of it every browser request arrives from the Central UI pod and no caller
// address is known. A proxy appends the address it saw, so the last entry is
// used; earlier entries are caller-supplied.
func ForwardedClientIP(r *http.Request) string {
	xff := r.Header.Values("X-Forwarded-For")
	if len(xff) == 0 {
		return ""
	}
	entries := strings.Split(xff[len(xff)-1], ",")
	return strings.TrimSpace(entries[len(entries)-1])
}

func SetClientIPInContext(ctx context.Context, ip string) context.Context {
	return context.WithValue(ctx, clientIPKey{}, ip)
}

// GetClientIPFromContext returns the caller address stored for this request, or "".
func GetClientIPFromContext(ctx context.Context) string {
	ip, _ := ctx.Value(clientIPKey{}).(string)
	return ip
}

// ClientIPMiddleware stores the caller address a reverse proxy forwarded, if
// any, in the request context for resolvers that rate-limit unauthenticated calls.
func ClientIPMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Request = c.Request.WithContext(SetClientIPInContext(c.Request.Context(), ForwardedClientIP(c.Request)))
		c.Next()
	}
}
