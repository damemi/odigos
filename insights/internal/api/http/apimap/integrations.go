package apimap

import (
	"github.com/odigos-io/odigos-enterprise/insights/internal/api/http/apitypes"
	"github.com/odigos-io/odigos-enterprise/insights/internal/notify"
	"github.com/odigos-io/odigos-enterprise/insights/internal/repo"
)

// FromDescriptor maps a backend catalog entry to its wire type.
func FromDescriptor(d notify.Descriptor) apitypes.IntegrationDescriptor {
	fields := make([]apitypes.IntegrationFieldSpec, len(d.Fields))
	for i, f := range d.Fields {
		fields[i] = apitypes.IntegrationFieldSpec{
			Key:         f.Key,
			Label:       f.Label,
			Type:        apitypes.IntegrationFieldType(f.Type),
			Required:    f.Required,
			Help:        f.Help,
			Placeholder: f.Placeholder,
			Default:     f.Default,
			Options:     f.Options,
		}
	}
	return apitypes.IntegrationDescriptor{
		Type:        d.Type,
		Name:        d.Name,
		Description: d.Description,
		DocsUrl:     d.DocsURL,
		Fields:      fields,
		Capabilities: apitypes.IntegrationCapabilities{
			NeedsHeartbeat:    d.Capabilities.NeedsHeartbeat,
			SupportsResolve:   d.Capabilities.SupportsResolve,
			ReturnsExternalId: d.Capabilities.ReturnsExternalID,
			MaxBatch:          d.Capabilities.MaxBatch,
		},
	}
}

// IntegrationCatalog wraps descriptors in the wire list envelope.
func IntegrationCatalog(in []notify.Descriptor) apitypes.IntegrationCatalog {
	items := make([]apitypes.IntegrationDescriptor, len(in))
	for i, d := range in {
		items[i] = FromDescriptor(d)
	}
	return apitypes.IntegrationCatalog{Count: len(items), Items: items}
}

// FromIntegration maps a stored integration to the wire type. fields is the
// backend's descriptor fields, used to redact secrets; health is the derived
// delivery health (zero when unknown).
func FromIntegration(in repo.Integration, fields []notify.FieldSpec, health repo.IntegrationHealth) apitypes.Integration {
	return apitypes.Integration{
		Id:        in.ID,
		Name:      in.Name,
		Type:      in.Type,
		Enabled:   in.Enabled,
		Config:    notify.Redact(fields, in.Config),
		Filters:   fromIntegrationFilters(in.Filters),
		Health:    FromIntegrationHealth(health),
		CreatedAt: in.CreatedAt,
		UpdatedAt: in.UpdatedAt,
	}
}

func fromIntegrationFilters(f repo.IntegrationFilters) apitypes.IntegrationFilters {
	var kinds []apitypes.FindingKind
	for _, k := range f.Kinds {
		kinds = append(kinds, apitypes.FindingKind(k))
	}
	return apitypes.IntegrationFilters{
		MinSeverity:     apitypes.Severity(f.MinSeverity),
		Kinds:           kinds,
		NotifyOnResolve: f.NotifyOnResolve,
	}
}

// ToIntegrationFilters maps the wire filters to the repo type.
func ToIntegrationFilters(f apitypes.IntegrationFilters) repo.IntegrationFilters {
	var kinds []string
	for _, k := range f.Kinds {
		kinds = append(kinds, string(k))
	}
	return repo.IntegrationFilters{
		MinSeverity:     string(f.MinSeverity),
		Kinds:           kinds,
		NotifyOnResolve: f.NotifyOnResolve,
	}
}

// FromIntegrationHealth maps derived health to the wire type.
func FromIntegrationHealth(h repo.IntegrationHealth) apitypes.IntegrationHealth {
	return apitypes.IntegrationHealth{
		LastSuccessAt:       h.LastSuccessAt,
		LastFailureAt:       h.LastFailureAt,
		LastError:           h.LastError,
		ConsecutiveFailures: h.ConsecutiveFailures,
		Pending:             int(h.Pending),
		Failed:              int(h.Failed),
		Dead:                int(h.Dead),
	}
}

// FromDelivery maps a ledger row to the wire type.
func FromDelivery(d repo.NotificationDispatch) apitypes.Delivery {
	return apitypes.Delivery{
		IntegrationId:     d.IntegrationID,
		FindingKey:        d.FindingKey,
		LastAction:        apitypes.DeliveryAction(d.LastAction),
		State:             apitypes.DeliveryState(d.State),
		Attempts:          d.Attempts,
		NextAttemptAt:     d.NextAttemptAt,
		LastError:         d.LastError,
		SentAt:            d.SentAt,
		OccurrencesAtSend: d.OccurrencesAtSend,
		SeverityAtSend:    d.SeverityAtSend,
		StatusAtSend:      d.StatusAtSend,
		ExternalId:        d.ExternalID,
		UpdatedAt:         d.UpdatedAt,
	}
}

// DeliveryList wraps ledger rows in the wire list envelope.
func DeliveryList(in []repo.NotificationDispatch) apitypes.DeliveryList {
	items := make([]apitypes.Delivery, len(in))
	for i, d := range in {
		items[i] = FromDelivery(d)
	}
	return apitypes.DeliveryList{Count: len(items), Items: items}
}

// FromDeliveryAttempt maps an audit row to the wire type.
func FromDeliveryAttempt(a repo.NotificationAttempt) apitypes.DeliveryAttempt {
	return apitypes.DeliveryAttempt{
		IntegrationId: a.IntegrationID,
		FindingKey:    a.FindingKey,
		Action:        apitypes.DeliveryAction(a.Action),
		Attempt:       a.Attempt,
		Outcome:       apitypes.DeliveryAttemptOutcome(a.Outcome),
		HttpStatus:    a.HTTPStatus,
		Error:         a.Error,
		DurationMs:    int(a.Duration.Milliseconds()),
		EventCount:    a.EventCount,
		AttemptedAt:   a.AttemptedAt,
	}
}

// DeliveryAttemptList wraps audit rows in the wire list envelope.
func DeliveryAttemptList(in []repo.NotificationAttempt) apitypes.DeliveryAttemptList {
	items := make([]apitypes.DeliveryAttempt, len(in))
	for i, a := range in {
		items[i] = FromDeliveryAttempt(a)
	}
	return apitypes.DeliveryAttemptList{Count: len(items), Items: items}
}
