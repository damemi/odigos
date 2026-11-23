package config

import (
	"fmt"

	"github.com/odigos-io/odigos/common"
)

const (
	signalfxRealm              = "SIGNALFX_REALM"
	signalfxUseCustomCa        = "SIGNALFX_USE_CUSTOM_CA"
	signalfxCaPem              = "SIGNALFX_CA_PEM"
	signalfxInsecureSkipVerify = "SIGNALFX_INSECURE_SKIP_VERIFY"
)

type SignalFx struct{}

func (s *SignalFx) DestType() common.DestinationType {
	return common.SignalFxDestinationType
}

func (s *SignalFx) ModifyConfig(dest ExporterConfigurer, currentConfig *Config) ([]string, error) {
	config := dest.GetConfig()
	realm := config[signalfxRealm]

	var pipelineNames []string

	if isMetricsEnabled(dest) {
		exporterName := "signalfx/" + dest.GetID()
		exporterConfig := GenericMap{
			"access_token": SecretEnvPlaceholder("SIGNALFX_ACCESS_TOKEN", dest),
			"realm":        realm,
			"api_url":      fmt.Sprintf("https://api.%s.signalfx.com", realm),
			"ingest_url":   fmt.Sprintf("https://ingest.%s.signalfx.com", realm),
		}
		if tlsConfig := signalfxTLSConfig(dest); tlsConfig != nil {
			exporterConfig["tls"] = tlsConfig
		}
		currentConfig.Exporters[exporterName] = exporterConfig

		pipelineName := "metrics/signalfx-" + dest.GetID()
		currentConfig.Service.Pipelines[pipelineName] = Pipeline{
			Exporters: []string{exporterName},
		}
		pipelineNames = append(pipelineNames, pipelineName)
	}

	// The signalfx exporter handles metrics and events only, so traces go to the same
	// Splunk Observability Cloud realm over OTLP/HTTP, as the legacy splunk destination does.
	if isTracingEnabled(dest) {
		exporterName := "otlp_http/signalfx-" + dest.GetID()
		exporterConfig := GenericMap{
			"traces_endpoint": fmt.Sprintf("https://ingest.%s.signalfx.com/v2/trace/otlp", realm),
			"headers": GenericMap{
				"X-SF-Token": SecretEnvPlaceholder("SIGNALFX_ACCESS_TOKEN", dest),
			},
		}
		if tlsConfig := signalfxTLSConfig(dest); tlsConfig != nil {
			exporterConfig["tls"] = tlsConfig
		}
		currentConfig.Exporters[exporterName] = exporterConfig

		pipelineName := "traces/signalfx-" + dest.GetID()
		currentConfig.Service.Pipelines[pipelineName] = Pipeline{
			Exporters: []string{exporterName},
		}
		pipelineNames = append(pipelineNames, pipelineName)
	}

	return pipelineNames, nil
}

// signalfxTLSConfig returns the exporter tls block when the destination customizes TLS, and nil
// otherwise. A fresh map is returned on every call so exporters never share one.
//
// SIGNALFX_CA_PEM is a secret field, so it is kept in the Destination Secret and never reaches
// GetConfig. The non-secret SIGNALFX_USE_CUSTOM_CA is what tells us a CA was supplied, and the
// certificate itself is read through the destination-scoped env var the gateway mounts from that
// Secret - the same shape as the Cassandra destination.
func signalfxTLSConfig(dest ExporterConfigurer) GenericMap {
	config := dest.GetConfig()
	useCustomCa := config[signalfxUseCustomCa] == "true"
	insecureSkipVerify := config[signalfxInsecureSkipVerify] == "true"

	if !useCustomCa && !insecureSkipVerify {
		return nil
	}

	tlsConfig := GenericMap{"insecure_skip_verify": insecureSkipVerify}
	if useCustomCa {
		tlsConfig["ca_pem"] = SecretEnvPlaceholder(signalfxCaPem, dest)
	}
	return tlsConfig
}
