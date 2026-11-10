package clustercollectorsgroup

import (
	"context"
	"slices"

	"github.com/odigos-io/odigos/api/k8sconsts"
	odigosv1 "github.com/odigos-io/odigos/api/odigos/v1alpha1"
	"github.com/odigos-io/odigos/common"
	"github.com/odigos-io/odigos/common/api/sampling"
	"github.com/odigos-io/odigos/instrumentor/controllers/utils"
	"github.com/odigos-io/odigos/k8sutils/pkg/env"
	k8slogger "github.com/odigos-io/odigos/k8sutils/pkg/logger"
	k8sutils "github.com/odigos-io/odigos/k8sutils/pkg/utils"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func getOwnMetricsConfig(odigosConfiguration *common.OdigosConfiguration, allDestinations *odigosv1.DestinationList) *odigosv1.CollectorsGroupMetricsCollectionSettings {
	ownMetricsInterval := "10s"
	if odigosConfiguration.MetricsSources != nil &&
		odigosConfiguration.MetricsSources.OdigosOwnMetrics != nil &&
		odigosConfiguration.MetricsSources.OdigosOwnMetrics.Interval != "" {
		ownMetricsInterval = odigosConfiguration.MetricsSources.OdigosOwnMetrics.Interval
	}

	ownMetricsLocalStorageEnabled := false
	if odigosConfiguration.OdigosOwnTelemetryStore == nil ||
		odigosConfiguration.OdigosOwnTelemetryStore.MetricsStoreDisabled == nil ||
		!*odigosConfiguration.OdigosOwnTelemetryStore.MetricsStoreDisabled {
		ownMetricsLocalStorageEnabled = true
	}

	sendToMetricsDestinations := false
	for _, destination := range allDestinations.Items {
		if destination.Spec.Disabled != nil && *destination.Spec.Disabled {
			continue
		}
		if !slices.Contains(destination.Spec.Signals, common.MetricsObservabilitySignal) {
			continue
		}
		if destination.Spec.MetricsSettings != nil &&
			destination.Spec.MetricsSettings.CollectOdigosOwnMetrics != nil &&
			*destination.Spec.MetricsSettings.CollectOdigosOwnMetrics {
			sendToMetricsDestinations = true
			break
		}
	}

	if !ownMetricsLocalStorageEnabled && !sendToMetricsDestinations {
		return nil
	}

	return &odigosv1.CollectorsGroupMetricsCollectionSettings{
		OdigosOwnMetrics: &odigosv1.OdigosOwnMetricsSettings{
			SendToOdigosMetricsStore:  ownMetricsLocalStorageEnabled,
			SendToMetricsDestinations: sendToMetricsDestinations,
			Interval:                  ownMetricsInterval,
		},
	}
}

func newClusterCollectorGroup(namespace string, resourcesSettings *odigosv1.CollectorsGroupResourcesSettings,
	serviceGraph common.ServiceGraphOptions, clusterMetricsEnabled *bool,
	httpsProxyAddress *string, nodeSelector *map[string]string, deploymentName string,
	metricsConfig *odigosv1.CollectorsGroupMetricsCollectionSettings, tailSampling *sampling.TailSamplingConfiguration,
	dryRun *bool, spanSamplingAttributes *sampling.SpanSamplingAttributesConfiguration,
	traceAggregation *common.TraceAggregationConfiguration) *odigosv1.CollectorsGroup {
	return &odigosv1.CollectorsGroup{
		TypeMeta: metav1.TypeMeta{
			Kind:       "CollectorsGroup",
			APIVersion: "odigos.io/v1alpha1",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      k8sconsts.OdigosClusterCollectorCollectorGroupName,
			Namespace: namespace,
		},
		Spec: odigosv1.CollectorsGroupSpec{
			Role:                                  odigosv1.CollectorsGroupRoleClusterGateway,
			CollectorOwnMetricsPort:               k8sconsts.OdigosClusterCollectorOwnTelemetryPortDefault,
			ResourcesSettings:                     *resourcesSettings,
			ServiceGraphDisabled:                  serviceGraph.Disabled,
			ServiceGraphExtraDimensions:           serviceGraph.ExtraDimensions,
			ServiceGraphVirtualNodePeerAttributes: serviceGraph.VirtualNodePeerAttributes,
			ClusterMetricsEnabled:                 clusterMetricsEnabled,
			HttpsProxyAddress:                     httpsProxyAddress,
			NodeSelector:                          nodeSelector,
			DeploymentName:                        deploymentName,
			Metrics:                               metricsConfig,
			TailSampling:                          tailSampling,
			SamplingDryRun:                        dryRun,
			SpanSamplingAttributes:                spanSamplingAttributes,
			TraceAggregation:                      traceAggregation,
		},
	}
}

func sync(ctx context.Context, c client.Client, scheme *runtime.Scheme) error {

	namespace := env.GetCurrentNamespace()

	odigosConfiguration, err := k8sutils.GetCurrentOdigosConfiguration(ctx, c)
	if err != nil {
		return err
	}
	resourceSettings := getGatewayResourceSettings(&odigosConfiguration)

	serviceGraph := odigosConfiguration.CollectorGateway.ServiceGraph
	// Default Disabled to false (feature is enabled by default).
	if serviceGraph.Disabled == nil {
		disabled := false
		serviceGraph.Disabled = &disabled
	}

	// default cluster metrics is disabled (clusterMetricsEnabled to false)
	clusterMetricsEnabled := odigosConfiguration.CollectorGateway.ClusterMetricsEnabled
	if clusterMetricsEnabled == nil {
		result := false
		clusterMetricsEnabled = &result
	}

	nodeSelector := odigosConfiguration.CollectorGateway.NodeSelector
	deploymentName := odigosConfiguration.CollectorGateway.DeploymentName
	allDestinations := &odigosv1.DestinationList{}
	if err := c.List(ctx, allDestinations); err != nil {
		return err
	}

	ownMetricsConfig := getOwnMetricsConfig(&odigosConfiguration, allDestinations)

	var tailSampling *sampling.TailSamplingConfiguration
	if isTailSamplingEnabled(ctx, c, &odigosConfiguration) {
		tailSampling = resolveTailSamplingConfig(true)
	}

	var dryRun *bool
	var spanSamplingAttributes *sampling.SpanSamplingAttributesConfiguration
	if odigosConfiguration.Sampling != nil {
		dryRun = odigosConfiguration.Sampling.DryRun
		spanSamplingAttributes = odigosConfiguration.Sampling.SpanSamplingAttributes
	}

	// cluster collector is always set and never deleted at the moment.
	// this is to accelerate spinup time and avoid errors while things are gradually being reconciled
	// and started.
	// in the future we might want to support a deployment of instrumentations only and allow user
	// to setup their own collectors, then we would avoid adding the cluster collector by default.
	clusterCollectorGroup := newClusterCollectorGroup(namespace, resourceSettings,
		*serviceGraph, clusterMetricsEnabled, odigosConfiguration.CollectorGateway.HttpsProxyAddress,
		nodeSelector, deploymentName, ownMetricsConfig, tailSampling, dryRun, spanSamplingAttributes,
		resolveTraceAggregation(k8slogger.FromContext(ctx), &odigosConfiguration))
	err = utils.SetOwnerControllerToInstrumentorDeployment(ctx, c, clusterCollectorGroup, scheme)
	if err != nil {
		return err
	}

	err = k8sutils.ApplyCollectorGroup(ctx, c, clusterCollectorGroup)
	if err != nil {
		return err
	}

	return nil
}

// isTailSamplingEnabled returns true if tail sampling is not globally disabled
// and either at least one Sampling CR exists or k8s health-probes sampling is enabled
// (health-probe noisy ops may be applied via collector tail sampling).
func isTailSamplingEnabled(ctx context.Context, c client.Client, odigosConfig *common.OdigosConfiguration) bool {
	logger := k8slogger.FromContext(ctx)

	if odigosConfig.Sampling != nil &&
		odigosConfig.Sampling.TailSampling != nil &&
		odigosConfig.Sampling.TailSampling.Disabled != nil &&
		*odigosConfig.Sampling.TailSampling.Disabled {
		return false
	}

	if odigosConfig.Sampling != nil &&
		odigosConfig.Sampling.K8sHealthProbesSampling != nil &&
		odigosConfig.Sampling.K8sHealthProbesSampling.Enabled != nil &&
		*odigosConfig.Sampling.K8sHealthProbesSampling.Enabled {
		return true
	}

	samplingList := &odigosv1.SamplingList{}
	if err := c.List(ctx, samplingList, &client.ListOptions{Namespace: env.GetCurrentNamespace()}); err != nil {
		logger.Error(err, "Failed to list Sampling CRs, tail sampling will be disabled")
		return false
	}

	return len(samplingList.Items) > 0
}

// resolveTailSamplingConfig returns the TailSamplingConfiguration written to the
// gateway CollectorsGroup. Trace aggregation lives in Spec.TraceAggregation.
func resolveTailSamplingConfig(enabled bool) *sampling.TailSamplingConfiguration {
	disabled := !enabled
	return &sampling.TailSamplingConfiguration{
		Disabled: &disabled,
	}
}
