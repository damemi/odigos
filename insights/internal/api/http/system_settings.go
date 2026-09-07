package http

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/odigos-io/odigos-enterprise/insights/internal/api/http/apimap"
	"github.com/odigos-io/odigos-enterprise/insights/internal/api/http/apitypes"
	"github.com/odigos-io/odigos-enterprise/insights/internal/repo"
)

var errSystemSettingsUnavailable = errAdminUnavailable

type systemSettingsValidationError struct{ error }

func (e systemSettingsValidationError) Unwrap() error { return e.error }

// LoadSystemSettings is the shared read path for GET /api/v1/system-settings
// and the debug System page (same payload the product UI consumes).
func (a *API) LoadSystemSettings(ctx context.Context) (apitypes.SystemSettings, error) {
	s, err := a.Repo.GetSystemSettings(ctx)
	if err != nil {
		return apitypes.SystemSettings{}, err
	}
	return apimap.FromSystemSettings(s), nil
}

// ApplySystemSettings is the shared write path for PUT /api/v1/system-settings
// and the debug System page: validate → optional inventory wipe → upsert →
// optional TTL → live reload.
func (a *API) ApplySystemSettings(ctx context.Context, body apitypes.SystemSettings) error {
	admin, err := a.adminOrErr()
	if err != nil {
		return err
	}

	s := apimap.ToSystemSettings(body)
	if err := s.Validate(); err != nil {
		return systemSettingsValidationError{err}
	}
	prev, _ := a.Repo.GetSystemSettings(ctx)
	s.TransactionInventoryEpoch = prev.TransactionInventoryEpoch

	dimsChanged := !repo.IdentityDimensionsEqual(prev.TransactionIdentityDimensions, s.TransactionIdentityDimensions)
	resetTxns := body.ResetTransactions != nil && *body.ResetTransactions
	wipeInventory := dimsChanged && resetTxns
	if wipeInventory {
		s.TransactionInventoryEpoch = prev.TransactionInventoryEpoch + 1
	}

	// The epoch is published before the wipe, not after: it is the only way
	// another replica learns its resident baselines describe a deleted
	// inventory. Wiping first leaves a window where a reloading replica still
	// reads the old epoch and old dimensions, so it neither clears nor stops
	// scoring - it keeps firing anomalies against baselines this call just
	// deleted, and keeps flushing them back into the emptied tables.
	if err := a.Repo.UpsertSystemSettings(ctx, s); err != nil {
		return err
	}
	if wipeInventory {
		if err := admin.DeleteAllTransactions(ctx); err != nil {
			return fmt.Errorf("reset transactions: %w", err)
		}
	}
	if s.ObservationRetentionDays != prev.ObservationRetentionDays {
		if err := a.Repo.SetObservationRetentionDays(ctx, s.ObservationRetentionDays); err != nil {
			return fmt.Errorf("update observation TTL: %w", err)
		}
	}
	if err := admin.ReloadSystemConfig(ctx); err != nil {
		return fmt.Errorf("reload system config: %w", err)
	}
	return nil
}

func (a *API) getSystemSettings(c *gin.Context) {
	s, err := a.LoadSystemSettings(c.Request.Context())
	if err != nil {
		writeError(c, http.StatusInternalServerError, err)
		return
	}
	c.JSON(http.StatusOK, s)
}

func (a *API) putSystemSettings(c *gin.Context) {
	if _, ok := a.requireAdmin(c); !ok {
		return
	}
	var body apitypes.SystemSettings
	if err := c.ShouldBindJSON(&body); err != nil {
		writeError(c, http.StatusBadRequest, err)
		return
	}
	if err := a.ApplySystemSettings(c.Request.Context(), body); err != nil {
		var ve systemSettingsValidationError
		switch {
		case errors.As(err, &ve):
			writeError(c, http.StatusUnprocessableEntity, ve)
		case errors.Is(err, errSystemSettingsUnavailable):
			writeError(c, http.StatusServiceUnavailable, err)
		default:
			writeError(c, http.StatusInternalServerError, err)
		}
		return
	}
	c.Status(http.StatusNoContent)
}
