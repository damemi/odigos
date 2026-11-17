package profiles

import (
	"github.com/odigos-io/odigos/common"
	"github.com/odigos-io/odigos/profiles/aggregators"
	"github.com/odigos-io/odigos/profiles/attributes"
	"github.com/odigos-io/odigos/profiles/instrumentation"
	"github.com/odigos-io/odigos/profiles/profile"
)

var AllProfiles = []profile.Profile{

	aggregators.KratosProfile,
	aggregators.GreatwallProfile,
	aggregators.InsightsProfile,

	attributes.CategoryAttributesProfile,
	attributes.CodeAttributesProfile,
	attributes.CopyScopeProfile,
	attributes.HostnameAsPodNameProfile,
	attributes.FullPayloadCollectionProfile,
	attributes.DbPayloadCollectionProfile,
	attributes.QueryOperationDetector,
	attributes.UrlTemplateProfile,
	attributes.InferDbAttributesProfile,
	attributes.SemconvUpgraderProfile,
	attributes.SemconvDynamoProfile,
	attributes.SemconvRedisProfile,
	attributes.ReduceSpanNameCardinalityProfile,

	instrumentation.AllowConcurrentAgents,
	instrumentation.EbpfLogCaptureProfile,
	instrumentation.InstrumentationV2Profile,
	instrumentation.JavaEbpfInstrumentationsProfile,
	instrumentation.JavaEnterpriseProfile,
	instrumentation.JavaNativeInstrumentationsProfile,
	instrumentation.LegacyDotNetProfile,
	instrumentation.LegacyRubyProfile,
	instrumentation.MountMethodK8sHostPathProfile,
	instrumentation.MountMethodK8sVirtualDevice,
	instrumentation.PodManifestEnvVarInjection,

	// golang
	instrumentation.DisableGinProfile,
	instrumentation.DisableGinNewProfile,

	// nodejs
	instrumentation.DisableNodejsExpressProfile,
	instrumentation.NodejsVerbosityFullProfile,
}

var ProfilesByName = map[common.ProfileName]profile.Profile{}

func init() {
	for _, p := range AllProfiles {
		ProfilesByName[p.ProfileName] = p
	}
}
