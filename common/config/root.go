package config

import (
	"fmt"
	"slices"
	"strings"

	"github.com/odigos-io/odigos/common"
)

var availableConfigers = []Configer{
	&AlibabaCloud{},
	&AlibabaCloudSLS{},
	&AppDynamics{},
	&AWSCloudWatch{},
	&AWSFirehose{},
	&AWSS3{},
	&AWSXRay{},
	&Axiom{},
	&AzureBlobStorage{},
	&AzureDataExplorer{},
	&AzureEventHubs{},
	&AzureMonitor{},
	&BetterStack{},
	&Bonree{},
	&Causely{},
	&Checkly{},
	&Chronosphere{},
	&Clickhouse{},
	&Cassandra{},
	&Coralogix{},
	&Dash0{},
	&Datadog{},
	&Debug{},
	&Dynamic{},
	&Dynatrace{},
	&ElasticAPM{},
	&Elasticsearch{},
	&GenericOTLP{},
	&GoogleCloud{},
	&GoogleCloudOTLP{},
	&GoogleManagedPrometheus{},
	&GoogleCloudStorage{},
	&GrafanaCloudLoki{},
	&GrafanaCloudPrometheus{},
	&GrafanaCloudTempo{},
	&Greptime{},
	&Groundcover{},
	&Honeycomb{},
	&HyperDX{},
	&Instana{},
	&Jaeger{},
	&Kafka{},
	&KloudMate{},
	&Last9{},
	&LaunchDarkly{},
	&Logzio{},
	&LocalFileArchive{},
	&Loki{},
	&Lumigo{},
	&Middleware{},
	&MicrosoftSentinel{},
	&Mock{},
	&NewRelic{},
	&Nop{},
	&Observe{},
	&OneUptime{},
	&OpenObserve{},
	&OpenStackSwift{},
	&Oracle{},
	&OTLPHttp{},
	&PostHog{},
	&Prometheus{},
	&Qryn{},
	&QrynOSS{},
	&Quickwit{},
	&Sentry{},
	&Seq{},
	&SignalFx{},
	&Signoz{},
	&SkyWalking{},
	&Splunk{},
	&SplunkOTLP{},
	&SumoLogic{},
	&Syslog{},
	&TelemetryHub{},
	&Tempo{},
	&Tingyun{},
	&Traceloop{},
	&Uptrace{},
	&VictoriaLogs{},
	&VictoriaMetricsCloud{},
	&VictoriaTraces{},
	&Pyroscope{},
}

type Configer interface {
	DestType() common.DestinationType
	ModifyConfig(dest ExporterConfigurer, currentConfig *Config) ([]string, error)
}

type ResourceStatuses struct {
	Destination map[string]error
	Processor   map[string]error
}

func LoadConfigers() (map[common.DestinationType]Configer, error) {
	configers := map[common.DestinationType]Configer{}
	for _, configer := range availableConfigers {
		if _, exists := configers[configer.DestType()]; exists {
			return nil, fmt.Errorf("duplicate configer for %s", configer.DestType())
		}

		configers[configer.DestType()] = configer
	}
	return configers, nil
}

func isSignalExists(dest SignalSpecific, signal common.ObservabilitySignal) bool {
	return slices.Contains(dest.GetSignals(), signal)
}

func isTracingEnabled(dest SignalSpecific) bool {
	return isSignalExists(dest, common.TracesObservabilitySignal)
}

func isMetricsEnabled(dest SignalSpecific) bool {
	return isSignalExists(dest, common.MetricsObservabilitySignal)
}

func isLoggingEnabled(dest SignalSpecific) bool {
	return isSignalExists(dest, common.LogsObservabilitySignal)
}

func isProfilingEnabled(dest SignalSpecific) bool {
	return isSignalExists(dest, common.ProfilesObservabilitySignal)
}

// addProfilesPipeline registers a "profiles/<prefix>-<id>" destination pipeline (exporter only) and
// returns its name. Like traces/metrics/logs destination pipelines, its receiver (the forward
// connector) is attached by the gateway builder, so it flows through the odigosrouterconnector.
func addProfilesPipeline(currentConfig *Config, prefix, destID, exporterName string) string {
	name := fmt.Sprintf("profiles/%s-%s", prefix, destID)
	currentConfig.Service.Pipelines[name] = Pipeline{
		Exporters: []string{exporterName},
	}
	return name
}

func addProtocol(s string) string {
	if strings.HasPrefix(s, "http://") || strings.HasPrefix(s, "https://") {
		return s
	}

	return fmt.Sprintf("http://%s", s)
}
