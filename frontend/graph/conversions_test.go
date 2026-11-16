package graph

import (
	"testing"

	"github.com/odigos-io/odigos/common"
	commonapisampling "github.com/odigos-io/odigos/common/api/sampling"
	"github.com/stretchr/testify/require"
)

func TestHeadSamplingOperationMatcherToModelPreservesQueryParams(t *testing.T) {
	valueExact := "{ health }"
	input := &commonapisampling.HeadSamplingOperationMatcher{
		HttpServer: &commonapisampling.HeadSamplingHttpServerOperationMatcher{
			Route:  "/graphql",
			Method: "POST",
			QueryParams: []commonapisampling.QueryParamMatcher{
				{
					Name:       "query",
					ValueExact: &valueExact,
				},
			},
		},
	}

	got := headSamplingOperationMatcherToModel(input)

	require.NotNil(t, got)
	require.NotNil(t, got.HTTPServer)
	require.Equal(t, "/graphql", *got.HTTPServer.Route)
	require.Equal(t, "POST", *got.HTTPServer.Method)
	require.Len(t, got.HTTPServer.QueryParams, 1)
	require.Equal(t, "query", got.HTTPServer.QueryParams[0].Name)
	require.NotNil(t, got.HTTPServer.QueryParams[0].ValueExact)
	require.Equal(t, valueExact, *got.HTTPServer.QueryParams[0].ValueExact)
}

// Settings reads each field by its Helm path; the Central backend URL's is centralProxy.centralBackendURL.
func TestEffectiveConfigToModelCentralBackendURLUnderHelmPath(t *testing.T) {
	result, err := EffectiveConfigToModel(&common.OdigosConfiguration{CentralBackendURL: "https://central.example.com:8081"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.CentralProxy == nil || result.CentralProxy.CentralBackendURL == nil || *result.CentralProxy.CentralBackendURL != "https://central.example.com:8081" {
		t.Fatalf("centralProxy.centralBackendURL = %+v, want the configured URL", result.CentralProxy)
	}
	if result.CentralBackendURL == nil || *result.CentralBackendURL != "https://central.example.com:8081" {
		t.Fatalf("centralBackendURL (kept for Central's queries) = %v, want the configured URL", result.CentralBackendURL)
	}
	found := false
	for _, entry := range result.Provenance {
		found = found || entry.HelmPath == "centralProxy.centralBackendURL"
	}
	if !found {
		t.Fatal("no provenance entry for centralProxy.centralBackendURL")
	}

	empty, err := EffectiveConfigToModel(&common.OdigosConfiguration{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if empty.CentralProxy != nil {
		t.Fatalf("centralProxy = %+v, want nil when no URL is set", empty.CentralProxy)
	}
}
