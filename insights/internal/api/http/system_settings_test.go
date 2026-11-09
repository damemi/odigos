package http

import (
	"context"
	"errors"
	"testing"

	"github.com/odigos-io/odigos-enterprise/insights/internal/api/http/apimap"
	"github.com/odigos-io/odigos-enterprise/insights/internal/api/http/apitypes"
	"github.com/odigos-io/odigos-enterprise/insights/internal/repo"
)

// stubSettingsRepo implements only the system-settings calls ApplySystemSettings
// makes; the embedded interface panics on anything else, which keeps the stub
// honest about the paths under test.
type stubSettingsRepo struct {
	repo.Repository
	stored   repo.SystemSettings
	getErr   error
	upserted []repo.SystemSettings
	ttlDays  []int
}

func (r *stubSettingsRepo) GetSystemSettings(context.Context) (repo.SystemSettings, error) {
	if r.getErr != nil {
		return repo.SystemSettings{}, r.getErr
	}
	return r.stored, nil
}

func (r *stubSettingsRepo) UpsertSystemSettings(_ context.Context, s repo.SystemSettings) error {
	r.upserted = append(r.upserted, s)
	return nil
}

func (r *stubSettingsRepo) SetObservationRetentionDays(_ context.Context, days int) error {
	r.ttlDays = append(r.ttlDays, days)
	return nil
}

type stubSettingsAdmin struct {
	Admin
	wipes   int
	reloads int
}

func (a *stubSettingsAdmin) DeleteAllTransactions(context.Context) error { a.wipes++; return nil }
func (a *stubSettingsAdmin) ReloadSystemConfig(context.Context) error    { a.reloads++; return nil }

func settingsAPI(t *testing.T, r *stubSettingsRepo) (*API, *stubSettingsAdmin) {
	t.Helper()
	admin := &stubSettingsAdmin{}
	api := &API{Repo: r}
	api.SetAdmin(admin)
	return api, admin
}

// storedSettings is a realistic persisted row: defaults, already reset a few
// times, so an epoch rollback is visible.
func storedSettings() repo.SystemSettings {
	s := repo.DefaultSystemSettings()
	s.TransactionInventoryEpoch = 7
	return s
}

func TestApplySystemSettings_ReadFailureDoesNotWipeInventory(t *testing.T) {
	r := &stubSettingsRepo{stored: storedSettings(), getErr: errors.New("clickhouse: read timeout")}
	api, admin := settingsAPI(t, r)

	// The caller's dimensions match what is stored, so the documented contract
	// ("reset_transactions is ignored when dimensions are unchanged") applies.
	body := apimap.FromSystemSettings(storedSettings())
	reset := true
	body.ResetTransactions = &reset

	err := api.ApplySystemSettings(context.Background(), body)
	if admin.wipes != 0 {
		t.Fatalf("inventory wiped %d time(s) after a failed read of the current settings", admin.wipes)
	}
	if len(r.upserted) != 0 {
		t.Fatalf("wrote settings despite a failed read: %+v", r.upserted)
	}
	if err == nil {
		t.Fatal("expected the failed settings read to be reported")
	}
}

func TestApplySystemSettings_UnchangedDimensionsIgnoreReset(t *testing.T) {
	r := &stubSettingsRepo{stored: storedSettings()}
	api, admin := settingsAPI(t, r)

	body := apimap.FromSystemSettings(storedSettings())
	reset := true
	body.ResetTransactions = &reset

	if err := api.ApplySystemSettings(context.Background(), body); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if admin.wipes != 0 {
		t.Fatalf("inventory wiped %d time(s) although identity dimensions did not change", admin.wipes)
	}
	if len(r.upserted) != 1 {
		t.Fatalf("upserted %d time(s), want 1", len(r.upserted))
	}
	if got := r.upserted[0].TransactionInventoryEpoch; got != 7 {
		t.Fatalf("inventory epoch = %d, want the stored 7", got)
	}
}

func TestApplySystemSettings_ChangedDimensionsWithResetWipesAndBumpsEpoch(t *testing.T) {
	r := &stubSettingsRepo{stored: storedSettings()}
	api, admin := settingsAPI(t, r)

	body := apimap.FromSystemSettings(storedSettings())
	body.Identity.TransactionIdentityDimensions = []apitypes.SystemTransactionIdentityDimension{
		{Key: repo.IdentityDimHTTPStatusCode, Enabled: true},
	}
	reset := true
	body.ResetTransactions = &reset

	if err := api.ApplySystemSettings(context.Background(), body); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if admin.wipes != 1 {
		t.Fatalf("inventory wiped %d time(s), want 1", admin.wipes)
	}
	if len(r.upserted) != 1 {
		t.Fatalf("upserted %d time(s), want 1", len(r.upserted))
	}
	if got := r.upserted[0].TransactionInventoryEpoch; got != 8 {
		t.Fatalf("inventory epoch = %d, want 8", got)
	}
}
