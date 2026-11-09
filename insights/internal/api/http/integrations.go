package http

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/odigos-io/odigos-enterprise/insights/internal/api/http/apimap"
	"github.com/odigos-io/odigos-enterprise/insights/internal/api/http/apitypes"
	"github.com/odigos-io/odigos-enterprise/insights/internal/notify"
	"github.com/odigos-io/odigos-enterprise/insights/internal/repo"
	"github.com/odigos-io/odigos-enterprise/insights/internal/risk"
)

var errIntegrationsUnavailable = errors.New("integrations are not available yet")
var errIntegrationNotFound = errors.New("integration not found")

// integrationValidationError wraps a config/body validation failure (422).
type integrationValidationError struct{ error }

func (e integrationValidationError) Unwrap() error { return e.error }

// integrationTestError wraps a failed connectivity test (422 with the
// backend's message).
type integrationTestError struct{ error }

func (e integrationTestError) Unwrap() error { return e.error }

const (
	defaultDeliveryLimit = 200
	maxDeliveryLimit     = 1000
	defaultAttemptHours  = 24
)

// SetIntegrationRegistry wires the backend catalog used to validate configs,
// build sinks for the test endpoint, and describe backends to clients.
func (a *API) SetIntegrationRegistry(reg *notify.Registry) { a.registry.Store(reg) }

func (a *API) registryOrErr() (*notify.Registry, error) {
	reg := a.registry.Load()
	if reg == nil {
		return nil, errIntegrationsUnavailable
	}
	return reg, nil
}

// IntegrationCatalog is the shared read path for GET /integrations/catalog and
// the debug UI's integration editor.
func (a *API) IntegrationCatalog() ([]notify.Descriptor, error) {
	reg, err := a.registryOrErr()
	if err != nil {
		return nil, err
	}
	return reg.Descriptors(), nil
}

// ListIntegrations returns every integration with redacted config and derived
// health. Shared by GET /integrations and the debug UI.
func (a *API) ListIntegrations(ctx context.Context) ([]apitypes.Integration, error) {
	reg, err := a.registryOrErr()
	if err != nil {
		return nil, err
	}
	rows, err := a.Repo.ListIntegrations(ctx)
	if err != nil {
		return nil, err
	}
	health, err := a.Repo.ListIntegrationHealth(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]apitypes.Integration, len(rows))
	for i, in := range rows {
		desc, _ := reg.Descriptor(in.Type)
		out[i] = apimap.FromIntegration(in, desc.Fields, health[in.ID])
	}
	return out, nil
}

// GetIntegration returns one integration (redacted) or errIntegrationNotFound.
func (a *API) GetIntegration(ctx context.Context, id string) (apitypes.Integration, error) {
	reg, err := a.registryOrErr()
	if err != nil {
		return apitypes.Integration{}, err
	}
	in, ok, err := a.Repo.GetIntegration(ctx, id)
	if err != nil {
		return apitypes.Integration{}, err
	}
	if !ok {
		return apitypes.Integration{}, errIntegrationNotFound
	}
	health, err := a.Repo.ListIntegrationHealth(ctx)
	if err != nil {
		return apitypes.Integration{}, err
	}
	desc, _ := reg.Descriptor(in.Type)
	return apimap.FromIntegration(in, desc.Fields, health[in.ID]), nil
}

// CreateIntegration validates the body against the backend's descriptor and
// stores it with a new id. Shared by POST /integrations and the debug UI.
func (a *API) CreateIntegration(ctx context.Context, body apitypes.IntegrationWrite) (apitypes.Integration, error) {
	reg, err := a.registryOrErr()
	if err != nil {
		return apitypes.Integration{}, err
	}
	typ := strings.TrimSpace(body.Type)
	desc, ok := reg.Descriptor(typ)
	if !ok {
		return apitypes.Integration{}, integrationValidationError{fmt.Errorf("unknown integration type %q", typ)}
	}
	in := repo.Integration{
		ID:      uuid.NewString(),
		Name:    strings.TrimSpace(body.Name),
		Type:    typ,
		Enabled: body.Enabled == nil || *body.Enabled,
		Config:  body.Config,
		Filters: apimap.ToIntegrationFilters(body.Filters),
	}
	if err := validateIntegration(desc, in); err != nil {
		return apitypes.Integration{}, err
	}
	if err := a.Repo.UpsertIntegration(ctx, in); err != nil {
		return apitypes.Integration{}, err
	}
	return a.GetIntegration(ctx, in.ID)
}

// UpdateIntegration replaces name/enabled/config/filters, keeping stored
// secrets for fields the body left empty or redacted. Type is immutable.
func (a *API) UpdateIntegration(ctx context.Context, id string, body apitypes.IntegrationWrite) (apitypes.Integration, error) {
	reg, err := a.registryOrErr()
	if err != nil {
		return apitypes.Integration{}, err
	}
	prev, ok, err := a.Repo.GetIntegration(ctx, id)
	if err != nil {
		return apitypes.Integration{}, err
	}
	if !ok {
		return apitypes.Integration{}, errIntegrationNotFound
	}
	desc, _ := reg.Descriptor(prev.Type)
	in := prev
	in.Name = strings.TrimSpace(body.Name)
	if body.Enabled != nil {
		in.Enabled = *body.Enabled
	}
	in.Config = notify.MergeSecrets(desc.Fields, body.Config, prev.Config)
	in.Filters = apimap.ToIntegrationFilters(body.Filters)
	if err := validateIntegration(desc, in); err != nil {
		return apitypes.Integration{}, err
	}
	if err := a.Repo.UpsertIntegration(ctx, in); err != nil {
		return apitypes.Integration{}, err
	}
	return a.GetIntegration(ctx, in.ID)
}

// DeleteIntegration removes an integration and its delivery state.
func (a *API) DeleteIntegration(ctx context.Context, id string) error {
	_, ok, err := a.Repo.GetIntegration(ctx, id)
	if err != nil {
		return err
	}
	if !ok {
		return errIntegrationNotFound
	}
	return a.Repo.DeleteIntegration(ctx, id)
}

// TestIntegration builds the sink from stored config and runs its
// connectivity check. Shared by POST /integrations/{id}/test and the debug UI.
func (a *API) TestIntegration(ctx context.Context, id string) error {
	reg, err := a.registryOrErr()
	if err != nil {
		return err
	}
	in, ok, err := a.Repo.GetIntegration(ctx, id)
	if err != nil {
		return err
	}
	if !ok {
		return errIntegrationNotFound
	}
	sink, _, err := reg.Build(in)
	if err != nil {
		return integrationValidationError{err}
	}
	tctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if err := sink.Test(tctx); err != nil {
		return integrationTestError{err}
	}
	return nil
}

// ListIntegrationDeliveries returns the ledger for one integration.
func (a *API) ListIntegrationDeliveries(ctx context.Context, id string, state string, limit int) ([]repo.NotificationDispatch, error) {
	if _, ok, err := a.Repo.GetIntegration(ctx, id); err != nil {
		return nil, err
	} else if !ok {
		return nil, errIntegrationNotFound
	}
	return a.Repo.ListNotificationDispatch(ctx, repo.DispatchListFilter{
		IntegrationID: id, State: repo.DispatchState(state), Limit: clampLimit(limit),
	})
}

// RetryIntegrationDeliveries re-arms failed/dead deliveries for one integration.
func (a *API) RetryIntegrationDeliveries(ctx context.Context, id string, findingKeys []string) error {
	if _, ok, err := a.Repo.GetIntegration(ctx, id); err != nil {
		return err
	} else if !ok {
		return errIntegrationNotFound
	}
	return a.Repo.ResetNotificationDispatch(ctx, id, findingKeys)
}

// ListIntegrationAttempts returns the audit for one integration, newest first.
func (a *API) ListIntegrationAttempts(ctx context.Context, id string, windowHours, limit int) ([]repo.NotificationAttempt, error) {
	if _, ok, err := a.Repo.GetIntegration(ctx, id); err != nil {
		return nil, err
	} else if !ok {
		return nil, errIntegrationNotFound
	}
	if windowHours <= 0 {
		windowHours = defaultAttemptHours
	}
	since := time.Now().Add(-time.Duration(windowHours) * time.Hour)
	return a.Repo.ListNotificationAttempts(ctx, id, since, clampLimit(limit))
}

// FindingDeliveries returns a finding's ledger rows across every integration.
// Shared by GET /findings/deliveries and the investigate pages in the debug UI.
func (a *API) FindingDeliveries(ctx context.Context, findingKey string) ([]repo.NotificationDispatch, error) {
	if findingKey == "" {
		return nil, fmt.Errorf("%w: finding_key is required", errBadRequest)
	}
	return a.Repo.ListNotificationDispatch(ctx, repo.DispatchListFilter{FindingKeys: []string{findingKey}, Limit: maxDeliveryLimit})
}

func validateIntegration(desc notify.Descriptor, in repo.Integration) error {
	if in.Name == "" {
		return integrationValidationError{errors.New("name is required")}
	}
	if _, err := notify.ValidateConfig(desc.Fields, in.Config); err != nil {
		return integrationValidationError{err}
	}
	if in.Filters.MinSeverity != "" {
		switch risk.Severity(in.Filters.MinSeverity) {
		case risk.SeverityInfo, risk.SeverityLow, risk.SeverityMedium, risk.SeverityHigh, risk.SeverityCritical:
		default:
			return integrationValidationError{fmt.Errorf("unknown min_severity %q", in.Filters.MinSeverity)}
		}
	}
	for _, k := range in.Filters.Kinds {
		if k != string(apitypes.Anomaly) && k != string(apitypes.Violation) {
			return integrationValidationError{fmt.Errorf("unknown kind %q", k)}
		}
	}
	return nil
}

func clampLimit(limit int) int {
	switch {
	case limit <= 0:
		return defaultDeliveryLimit
	case limit > maxDeliveryLimit:
		return maxDeliveryLimit
	}
	return limit
}

// writeIntegrationError maps integration service errors to HTTP statuses.
func writeIntegrationError(c *gin.Context, err error) {
	var ve integrationValidationError
	var te integrationTestError
	switch {
	case errors.Is(err, errIntegrationsUnavailable):
		writeError(c, http.StatusServiceUnavailable, err)
	case errors.Is(err, errIntegrationNotFound):
		writeError(c, http.StatusNotFound, err)
	case errors.As(err, &ve), errors.As(err, &te):
		writeError(c, http.StatusUnprocessableEntity, err)
	default:
		writeAPIError(c, err)
	}
}

// ---------- gin handlers ----------

func (a *API) getIntegrationCatalog(c *gin.Context) {
	descs, err := a.IntegrationCatalog()
	if err != nil {
		writeIntegrationError(c, err)
		return
	}
	c.JSON(http.StatusOK, apimap.IntegrationCatalog(descs))
}

func (a *API) listIntegrations(c *gin.Context) {
	items, err := a.ListIntegrations(c.Request.Context())
	if err != nil {
		writeIntegrationError(c, err)
		return
	}
	c.JSON(http.StatusOK, apitypes.IntegrationList{Count: len(items), Items: items})
}

func (a *API) createIntegration(c *gin.Context) {
	var body apitypes.IntegrationWrite
	if err := c.ShouldBindJSON(&body); err != nil {
		writeError(c, http.StatusBadRequest, err)
		return
	}
	out, err := a.CreateIntegration(c.Request.Context(), body)
	if err != nil {
		writeIntegrationError(c, err)
		return
	}
	c.JSON(http.StatusCreated, out)
}

func (a *API) getIntegration(c *gin.Context) {
	out, err := a.GetIntegration(c.Request.Context(), c.Param("integration_id"))
	if err != nil {
		writeIntegrationError(c, err)
		return
	}
	c.JSON(http.StatusOK, out)
}

func (a *API) updateIntegration(c *gin.Context) {
	var body apitypes.IntegrationWrite
	if err := c.ShouldBindJSON(&body); err != nil {
		writeError(c, http.StatusBadRequest, err)
		return
	}
	out, err := a.UpdateIntegration(c.Request.Context(), c.Param("integration_id"), body)
	if err != nil {
		writeIntegrationError(c, err)
		return
	}
	c.JSON(http.StatusOK, out)
}

func (a *API) deleteIntegration(c *gin.Context) {
	if err := a.DeleteIntegration(c.Request.Context(), c.Param("integration_id")); err != nil {
		writeIntegrationError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (a *API) testIntegration(c *gin.Context) {
	if err := a.TestIntegration(c.Request.Context(), c.Param("integration_id")); err != nil {
		writeIntegrationError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (a *API) listIntegrationDeliveries(c *gin.Context) {
	var params apitypes.ListIntegrationDeliveriesParams
	if err := c.ShouldBindQuery(&params); err != nil {
		writeBindError(c, err)
		return
	}
	rows, err := a.ListIntegrationDeliveries(c.Request.Context(), c.Param("integration_id"), string(params.State), params.Limit)
	if err != nil {
		writeIntegrationError(c, err)
		return
	}
	c.JSON(http.StatusOK, apimap.DeliveryList(rows))
}

func (a *API) retryIntegrationDeliveries(c *gin.Context) {
	var body apitypes.DeliveryRetryRequest
	if c.Request.ContentLength != 0 {
		if err := c.ShouldBindJSON(&body); err != nil {
			writeError(c, http.StatusBadRequest, err)
			return
		}
	}
	if err := a.RetryIntegrationDeliveries(c.Request.Context(), c.Param("integration_id"), body.FindingKeys); err != nil {
		writeIntegrationError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (a *API) listIntegrationAttempts(c *gin.Context) {
	var params apitypes.ListIntegrationAttemptsParams
	if err := c.ShouldBindQuery(&params); err != nil {
		writeBindError(c, err)
		return
	}
	rows, err := a.ListIntegrationAttempts(c.Request.Context(), c.Param("integration_id"), params.WindowHours, params.Limit)
	if err != nil {
		writeIntegrationError(c, err)
		return
	}
	c.JSON(http.StatusOK, apimap.DeliveryAttemptList(rows))
}

func (a *API) getFindingDeliveries(c *gin.Context) {
	rows, err := a.FindingDeliveries(c.Request.Context(), c.Query("finding_key"))
	if err != nil {
		writeIntegrationError(c, err)
		return
	}
	c.JSON(http.StatusOK, apimap.DeliveryList(rows))
}
