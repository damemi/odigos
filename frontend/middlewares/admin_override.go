package middlewares

import (
	"context"

	"github.com/odigos-io/odigos/api/k8sconsts"
)

const AdminOverrideHeader = k8sconsts.CentralProxyAdminOverrideHeader

type ctxKey string

const ctxKeyAdminOverride ctxKey = "odigos_admin_override"

func WithAdminOverride(ctx context.Context) context.Context {
	return context.WithValue(ctx, ctxKeyAdminOverride, true)
}

func AdminOverrideFromContext(ctx context.Context) bool {
	v, ok := ctx.Value(ctxKeyAdminOverride).(bool)
	return ok && v
}
