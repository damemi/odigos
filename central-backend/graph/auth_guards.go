package graph

import (
	"context"

	"github.com/odigos-io/central-backend/internal/utils"
)

// requireIdentityProviderAccess allows identity provider changes by ADMIN/OWNER.
// Before any user exists, the sign-in page lets the installer set up SSO without a
// session (the first SSO user then becomes admin), so anonymous callers are allowed
// only until the system is initialized. An error reading that state denies access.
func (r *mutationResolver) requireIdentityProviderAccess(ctx context.Context) error {
	adminErr := utils.RequireAdmin(ctx)
	if adminErr == nil {
		return nil
	}
	initialized, err := r.AdminService.IsSystemInitialized()
	if err != nil || initialized {
		return adminErr
	}
	return nil
}
