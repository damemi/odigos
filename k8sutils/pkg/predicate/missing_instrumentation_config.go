package predicate

import (
	"context"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	cr_predicate "sigs.k8s.io/controller-runtime/pkg/predicate"

	odigosv1 "github.com/odigos-io/odigos/api/odigos/v1alpha1"
	commonlogger "github.com/odigos-io/odigos/common/logger"
	sourceutils "github.com/odigos-io/odigos/k8sutils/pkg/source"
	"github.com/odigos-io/odigos/k8sutils/pkg/workload"
)

// MissingInstrumentationConfigPredicate allows update events when the workload's InstrumentationConfig
// does not exist and the workload is still covered by an active Source.
type MissingInstrumentationConfigPredicate struct {
	Client client.Client
}

func (p MissingInstrumentationConfigPredicate) Create(e event.CreateEvent) bool {
	return false
}

func (p MissingInstrumentationConfigPredicate) Update(e event.UpdateEvent) bool {
	return p.workloadMissingInstrumentationConfig(e.ObjectNew)
}

func (p MissingInstrumentationConfigPredicate) Delete(e event.DeleteEvent) bool {
	return false
}

func (p MissingInstrumentationConfigPredicate) Generic(e event.GenericEvent) bool {
	return false
}

func (p MissingInstrumentationConfigPredicate) workloadMissingInstrumentationConfig(obj client.Object) bool {
	logger := commonlogger.LoggerCompat().With("subsystem", "missing-ic-predicate")

	if obj == nil {
		logger.Debug("rejecting update event: object is nil")
		return false
	}

	pw, err := workload.PodWorkloadFromObject(obj)
	if err != nil {
		logger.Debug("rejecting update event: unsupported workload object",
			"namespace", obj.GetNamespace(), "name", obj.GetName(), "err", err)
		return false
	}

	icName := workload.CalculateWorkloadRuntimeObjectName(pw.Name, pw.Kind)
	ic := &odigosv1.InstrumentationConfig{}
	err = p.Client.Get(context.Background(), client.ObjectKey{Namespace: pw.Namespace, Name: icName}, ic)
	if err == nil {
		logger.Debug("rejecting update event: instrumentation config already exists",
			"workload", pw.Name, "namespace", pw.Namespace, "kind", pw.Kind, "icName", icName)
		return false
	}
	if !apierrors.IsNotFound(err) {
		logger.Debug("rejecting update event: failed to get instrumentation config",
			"workload", pw.Name, "namespace", pw.Namespace, "kind", pw.Kind, "icName", icName, "err", err)
		return false
	}

	sources, err := odigosv1.GetSources(context.Background(), p.Client, pw)
	enabled, _, err := sourceutils.IsObjectInstrumentedBySource(context.Background(), sources, err)
	if err != nil {
		logger.Debug("rejecting update event: failed to evaluate source coverage",
			"workload", pw.Name, "namespace", pw.Namespace, "kind", pw.Kind, "err", err)
		return false
	}
	if !enabled {
		logger.Debug("rejecting update event: workload is not covered by an active source",
			"workload", pw.Name, "namespace", pw.Namespace, "kind", pw.Kind, "icName", icName)
		return false
	}

	logger.Debug("allowing update event: instrumentation config missing and source still applies",
		"workload", pw.Name, "namespace", pw.Namespace, "kind", pw.Kind, "icName", icName)
	return true
}

// WorkloadCreateOrMissingInstrumentationConfig reconciles workload creates and updates that happen while the
// workload's InstrumentationConfig is missing (for example after a GitOps replace cascades IC deletion).
func WorkloadCreateOrMissingInstrumentationConfig(c client.Client) cr_predicate.Predicate {
	return cr_predicate.Or(&CreationPredicate{}, &MissingInstrumentationConfigPredicate{Client: c})
}

var _ cr_predicate.Predicate = &MissingInstrumentationConfigPredicate{}
