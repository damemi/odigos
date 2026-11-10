package instrumentor

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/odigos-io/odigos/connectors/anypoint"
	baseapi "github.com/odigos-io/odigos/connectors/base/api"
	domaindest "github.com/odigos-io/odigos/connectors/base/domain/destination"
	"github.com/odigos-io/odigos/connectors/base/domain/plan"
	basestore "github.com/odigos-io/odigos/connectors/base/store"
	store "github.com/odigos-io/odigos/connectors/providers/mulesoft/pkg/workload/store"
)

// observerInventory is the slice of the store the observer reads and writes.
type observerInventory interface {
	ListApplications(ctx context.Context, kinds ...string) ([]store.Application, error)
	ListSources(ctx context.Context) ([]basestore.SourceRecord, error)
	OTLPExportTarget(ctx context.Context) (domaindest.OTLPTarget, bool, error)
	SetObservation(ctx context.Context, externalID string, obs store.Observation) error
	SetInstrumented(ctx context.Context, externalID, origin string) error
}

// Observer judges, read-only, whether a manual-mode application carries the property set the connector
// publishes for it. The base runs it only for a type in manual mode; in automatic mode the reconciler already
// converges the same properties.
//
// Its verdict is what makes manual mode honest: the ODIGOS_IAC marker alone says the customer applied
// something, not that they applied the right endpoint or service name.
type Observer struct {
	kind     string
	store    observerInventory
	client   *anypoint.Client
	orgID    string
	settings func() Settings
}

// NewObserver builds the observer for one Application Manager kind.
func NewObserver(kind string, st observerInventory, client *anypoint.Client, orgID string, settings func() Settings) *Observer {
	return &Observer{kind: kind, store: st, client: client, orgID: orgID, settings: settings}
}

// Observe implements baseapi.InstrumentationObserver.
func (o *Observer) Observe(ctx context.Context) error {
	sources, err := o.store.ListSources(ctx)
	if err != nil {
		return err
	}
	enabled := map[string]string{}
	for _, s := range sources {
		if s.Kind == o.kind && s.Enabled && s.ExternalResourceID != "" {
			enabled[s.ExternalResourceID] = s.ReportedServiceName()
		}
	}
	if len(enabled) == 0 {
		return nil
	}
	apps, err := o.store.ListApplications(ctx, o.kind)
	if err != nil {
		return err
	}
	dest, haveDest, err := o.store.OTLPExportTarget(ctx)
	if err != nil {
		haveDest = false
	}
	ratio := o.settings().SampleRatio

	var errs []error
	for _, app := range apps {
		serviceName, want := enabled[app.ExternalID]
		if !want {
			continue
		}
		if serviceName == "" {
			serviceName = app.AppName
		}
		obs, origin, err := o.observe(ctx, app, serviceName, dest, haveDest, ratio)
		if err != nil {
			errs = append(errs, err)
		}
		if err := o.store.SetObservation(ctx, app.ExternalID, obs); err != nil {
			errs = append(errs, fmt.Errorf("persist observation for %s: %w", app.AppName, err))
		}
		if obs.Error == "" {
			if err := o.store.SetInstrumented(ctx, app.ExternalID, origin); err != nil {
				errs = append(errs, err)
			}
		}
	}
	return errors.Join(errs...)
}

func (o *Observer) observe(ctx context.Context, app store.Application, serviceName string, dest domaindest.OTLPTarget, haveDest bool, ratio float64) (store.Observation, string, error) {
	prev := app.Observation
	// A failed read is not drift: keep the last confirmed verdict and record why this pass could not look.
	keep := func(msg string) store.Observation {
		return store.Observation{Conformance: prev.Conformance, Missing: prev.Missing, Since: prev.Since, Error: msg}
	}
	if o.kind == store.KindOmniGateway {
		return o.observeGateway(ctx, app, serviceName, dest, haveDest)
	}

	dep, err := o.client.GetDeployment(ctx, o.orgID, app.EnvironmentID, app.ExternalID)
	if err != nil {
		return keep("read deployment: " + anypoint.Summarize(err)), "", fmt.Errorf("observe %s: %s", app.AppName, anypoint.Summarize(err))
	}
	if !haveDest {
		return keep("no OTLP destination is configured, so the export settings cannot be checked"), "", nil
	}

	values := ManualValues(app, o.orgID, dest, haveDest, ratio)
	values[plan.PlaceholderServiceName] = serviceName
	pl, err := ManualPlan(o.kind, SignalsFor(dest, haveDest), metricsSupported(dep.RuntimeVersion()))
	if err != nil {
		return keep(err.Error()), "", err
	}
	props, secure := dep.Properties(), dep.SecurePropertyKeys()
	verdict, missing := Judge(pl, values, props, secure)

	since := time.Now().UTC()
	if prev.Conformance == verdict && !prev.Since.IsZero() {
		since = prev.Since
	}
	return store.Observation{
		Conformance: verdict,
		Missing:     missing,
		Since:       since,
		// Settling only while an update is rolling out: a FAILED deployment will not converge by waiting.
		Settling: dep.Status != anypoint.DeploymentApplied && dep.Status != anypoint.DeploymentFailed,
	}, store.MarkerOrigin(props, secure, app.ExternalID), nil
}

// Judge compares an application's properties with the manual plan resolved for it.
//
// No Odigos marker at all is Absent: nothing says the customer applied the instructions. Otherwise each plan
// property is checked — a secure property counts as present, since its value cannot be read back — and the
// stale ones are named in the base's component vocabulary. Properties the customer added beyond the plan are
// theirs and never count against them.
func Judge(pl plan.Plan, values map[string]string, props map[string]string, secureKeys []string) (baseapi.Conformance, []string) {
	has := func(k string) bool {
		_, ok := props[k]
		return ok || slices.Contains(secureKeys, k)
	}
	if !has(store.PropOdigosIaC) && !has(store.PropOdigosManaged) {
		return baseapi.ConformanceAbsent, nil
	}

	missing := map[string]bool{}
	for _, f := range pl.Fragments {
		for _, e := range f.Env {
			if e.Name == store.PropOdigosIaC {
				continue // presence already checked; either marker attributes the application
			}
			want := plan.Substitute(e.Value, values)
			if slices.Contains(secureKeys, e.Name) {
				continue
			}
			got, ok := props[e.Name]
			switch {
			case e.Merge == plan.MergeAppend:
				if !ok || !containsAll(got, want, e.Separator) {
					missing[componentFor(e.Name)] = true
				}
			case !ok || got != want:
				missing[componentFor(e.Name)] = true
			}
		}
	}
	if len(missing) == 0 {
		return baseapi.ConformanceConformant, nil
	}
	out := make([]string, 0, len(missing))
	for c := range missing {
		out = append(out, c)
	}
	slices.Sort(out)
	return baseapi.ConformancePartial, out
}

// containsAll reports whether every element of want (split on sep) is one of got's elements: our resource
// attributes are appended to the customer's, so their order and their own attributes do not matter.
func containsAll(got, want, sep string) bool {
	have := map[string]bool{}
	for _, g := range strings.Split(got, sep) {
		have[strings.TrimSpace(g)] = true
	}
	for _, w := range strings.Split(want, sep) {
		if !have[strings.TrimSpace(w)] {
			return false
		}
	}
	return true
}

func componentFor(prop string) string {
	switch prop {
	case PropServiceName:
		return baseapi.ComponentServiceName
	case PropResourceAttributes:
		return baseapi.ComponentResourceAttributes
	default:
		return baseapi.ComponentCollectorExport
	}
}
