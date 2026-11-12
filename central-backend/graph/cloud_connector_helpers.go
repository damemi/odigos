package graph

import (
	"fmt"

	"github.com/odigos-io/odigos/central-backend/graph/model"
	"github.com/odigos-io/odigos/connectors/spec"
	"github.com/odigos-io/odigos/connectors/testconnection"
	cloudconnectorsv1 "github.com/odigos-io/odigos/api/cloud-connectors/v1alpha1"
)

const (
	featureDiscovery        = "discovery"
	featureInstrumentation  = "instrumentation"
	featureComputePlatforms = "computePlatforms"
	featureGateway          = "gateway"
	// Collector mode selects management reads in manual and automatic mode, and
	// deployment writes only in automatic mode. ui-kit adds these feature keys to
	// the connector identity's policy preview for the selected mode.
	featureCollectorManagement = "collectorManagement"
	featureCollectorDeployment = "collectorDeployment"
)

// cloudConnectorProvidersFromSpecs maps the embedded provider specs to their GraphQL
// model. Specs are already ordered by provider name (spec.All()).
func cloudConnectorProvidersFromSpecs(specs []spec.CloudConnectorSpec) []*model.CloudConnectorProvider {
	out := make([]*model.CloudConnectorProvider, 0, len(specs))
	for _, s := range specs {
		out = append(out, cloudConnectorProviderFromSpec(s))
	}
	return out
}

func cloudConnectorProviderFromSpec(s spec.CloudConnectorSpec) *model.CloudConnectorProvider {
	return &model.CloudConnectorProvider{
		Provider:        s.Provider,
		DisplayName:     s.DisplayName,
		ConnectorFields: cloudConnectorFields(s.Connector.Fields),
		BootstrapRegion: &model.CloudConnectorBootstrapRegion{DefaultRegion: s.Connector.BootstrapRegion.DefaultRegion, SupportedRegions: nonNilStrings(s.Connector.BootstrapRegion.SupportedRegions)},
		Auth: &model.CloudConnectorAuth{
			Permissions:   nonNilStrings(s.Auth.Permissions),
			DefaultMethod: s.Auth.DefaultMethod,
			Methods:       cloudConnectorAuthMethods(s.Auth.Methods),
		},
		Features: cloudConnectorFeatures(s),
		Setup:    cloudConnectorSetup(s.Setup),
	}
}

// cloudConnectorSetup maps the spec's declarative onboarding artifacts to their
// GraphQL model. The UI renders these client-side (filling {{permissions}} and
// scalar {{vars}} from the selected access level / form), so the connector owns
// the artifact definition and the UI stays provider-agnostic.
//
// Every field is carried through uninterpreted, `language` included: in an
// instrumentation section one variant exists per runtime language, so dropping it
// would leave several artifacts indistinguishable by (section, variant) and make
// the catalog look like it declared the same snippet four times.
func cloudConnectorSetup(s spec.Setup) *model.CloudConnectorSetup {
	artifacts := make([]*model.CloudConnectorSetupArtifact, 0, len(s.Artifacts))
	for _, a := range s.Artifacts {
		artifacts = append(artifacts, &model.CloudConnectorSetupArtifact{
			ID:            a.ID,
			Section:       a.Section,
			Variant:       a.Variant,
			VariantLabel:  a.VariantLabel,
			Language:      a.Language,
			Title:         a.Title,
			Description:   a.Description,
			Steps:         nonNilStrings(a.Steps),
			Format:        a.Format,
			Template:      a.Template,
			ItemTemplate:  a.ItemTemplate,
			ItemSeparator: a.ItemSeparator,
		})
	}
	return &model.CloudConnectorSetup{Subtitle: s.Subtitle, Artifacts: artifacts}
}

func cloudConnectorAuthMethods(methods []spec.AuthMethod) []*model.CloudConnectorAuthMethod {
	out := make([]*model.CloudConnectorAuthMethod, 0, len(methods))
	for _, m := range methods {
		out = append(out, &model.CloudConnectorAuthMethod{
			ID:          m.ID,
			DisplayName: m.DisplayName,
			Description: m.Description,
			BaseMethods: nonNilStrings(m.BaseMethods),
			Fields:      cloudConnectorFields(m.Fields),
		})
	}
	return out
}

// cloudConnectorFeatures emits the UI-facing feature catalog from the spec's three
// domains (workloads / computePlatforms / collectors) plus gateway, in a stable order.
//
// The list is explicit rather than reflected over the spec, so a domain added to the spec reaches no
// client until it is named here. That is what kept the managed-Collector permissions out of every
// customer's policy while the domain was being declared, and it is why forwarding them is a deliberate
// step rather than a side effect.
//
// A managed Collector needs mode-scoped permissions on the existing connector identity. Both features
// reach the client that renders the policy so it can include reads for manual mode and writes for auto.
func cloudConnectorFeatures(s spec.CloudConnectorSpec) []*model.CloudConnectorFeature {
	return []*model.CloudConnectorFeature{
		cloudConnectorFeatureFromSpec(featureDiscovery, s.Workloads.Discovery),
		cloudConnectorFeatureFromSpec(featureInstrumentation, s.Workloads.Instrumentation),
		cloudConnectorFeatureFromSpec(featureComputePlatforms, s.ComputePlatforms.Discovery),
		cloudConnectorFeatureFromSpec(featureCollectorManagement, s.Collectors.Management),
		cloudConnectorFeatureFromSpec(featureCollectorDeployment, s.Collectors.Deployment),
		cloudConnectorFeatureFromSpec(featureGateway, s.Gateway),
	}
}

func cloudConnectorFeatureFromSpec(key string, f spec.Feature) *model.CloudConnectorFeature {
	return &model.CloudConnectorFeature{
		Key:         key,
		Enabled:     f.Enabled,
		DisplayName: f.DisplayName,
		Description: f.Description,
		Permissions: nonNilStrings(f.Permissions),
		Types:       cloudConnectorTypes(f.Types),
	}
}

func cloudConnectorTypes(types []spec.Type) []*model.CloudConnectorType {
	out := make([]*model.CloudConnectorType, 0, len(types))
	for _, t := range types {
		out = append(out, &model.CloudConnectorType{
			Name:        t.Name,
			DisplayName: t.DisplayName,
			Description: t.Description,
			Permissions: nonNilStrings(t.Permissions),
			Method:      t.Method,
			Implemented: t.IsImplemented(),
		})
	}
	return out
}

// cloudConnectorFields maps the spec's form fields to their GraphQL model.
//
// ShowInUI has to be carried: it is the spec's only way to say a field belongs behind a
// disclosure rather than on the common path (the AWS sessionToken, which only temporary
// access keys use). Dropping it here is what made `showInUi: false` inert and put every
// declared field on screen. It goes through the accessor, not the raw pointer, so a field
// that omits the key stays visible.
func cloudConnectorFields(fields []spec.Field) []*model.CloudConnectorField {
	out := make([]*model.CloudConnectorField, 0, len(fields))
	for _, f := range fields {
		out = append(out, &model.CloudConnectorField{
			Name:           f.Name,
			DisplayName:    f.DisplayName,
			ComponentType:  f.ComponentType,
			ComponentProps: f.ComponentProps,
			Required:       f.Required,
			Secret:         f.Secret,
			Tooltip:        f.Tooltip,
			InitialValue:   f.InitialValue,
			ShowInUI:       f.IsShownInUi(),
			EnvVar:         f.EnvVar,
		})
	}
	return out
}

// nonNilStrings normalizes a nil slice to an empty one so non-null GraphQL list
// fields ([String!]!) never resolve to null.
func nonNilStrings(in []string) []string {
	if in == nil {
		return []string{}
	}
	return in
}

// credentialsFromMap coerces the JSON credentials map (values are provider auth
// field values) into the string-keyed map the tester expects. Non-string values
// are stringified; empty values are dropped so auth resolution treats them as
// absent (matching spec.AuthConfig.Resolve).
func credentialsFromMap(in map[string]any) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		if v == nil {
			continue
		}
		s, ok := v.(string)
		if !ok {
			s = fmt.Sprintf("%v", v)
		}
		if s == "" {
			continue
		}
		out[k] = s
	}
	return out
}

// capabilitiesFromInput maps the GraphQL capability inputs to spec.Capabilities.
func capabilitiesFromInput(provider string, in []*model.CloudConnectorCapabilityInput) spec.Capabilities {
	if len(in) == 0 {
		return spec.Capabilities{}
	}
	s, ok := spec.Get(provider)
	if !ok {
		return spec.Capabilities{}
	}
	var out spec.Capabilities
	for _, c := range in {
		if c == nil {
			continue
		}
		if specHasType(s.Workloads.Discovery.Types, c.Type) {
			out.Workloads = append(out.Workloads, spec.WorkloadCapability{
				Type:            c.Type,
				Discovery:       c.Discovery,
				Instrumentation: c.Instrumentation,
			})
			continue
		}
		if specHasType(s.ComputePlatforms.Discovery.Types, c.Type) {
			out.ComputePlatforms = append(out.ComputePlatforms, spec.ComputePlatformCapability{
				Type:         c.Type,
				Discovery:    c.Discovery,
				Installation: c.ComputePlatforms,
			})
		}
	}
	return out
}

func collectorCapabilitiesFromInput(in []*model.CloudConnectorCollectorCapabilityInput) []spec.CollectorCapability {
	if in == nil {
		return nil
	}
	out := make([]spec.CollectorCapability, 0, len(in))
	for _, c := range in {
		if c == nil {
			continue
		}
		out = append(out, spec.CollectorCapability{
			Type: c.Type, Management: c.Management, Deployment: c.Deployment,
		})
	}
	return out
}

func crdCollectorCapabilities(in []spec.CollectorCapability) []cloudconnectorsv1.CloudConnectorCollectorCapability {
	if in == nil {
		return nil
	}
	out := make([]cloudconnectorsv1.CloudConnectorCollectorCapability, 0, len(in))
	for _, c := range in {
		out = append(out, cloudconnectorsv1.CloudConnectorCollectorCapability{
			Type: c.Type, Management: c.Management, Deployment: c.Deployment,
		})
	}
	return out
}

// crdCapabilitiesFromInput maps GraphQL capability inputs to the CRD's split
// workload / compute-platform allow-lists, using the provider spec to decide
// which domain each type belongs to.
func crdCapabilitiesFromInput(provider string, in []*model.CloudConnectorCapabilityInput) (workloads []cloudconnectorsv1.CloudConnectorWorkloadCapability, computePlatforms []cloudconnectorsv1.CloudConnectorComputePlatformCapability) {
	if len(in) == 0 {
		return nil, nil
	}
	s, ok := spec.Get(provider)
	if !ok {
		return nil, nil
	}
	for _, c := range in {
		if c == nil {
			continue
		}
		if specHasType(s.Workloads.Discovery.Types, c.Type) {
			workloads = append(workloads, cloudconnectorsv1.CloudConnectorWorkloadCapability{
				Type:            c.Type,
				Discovery:       c.Discovery,
				Instrumentation: c.Instrumentation,
			})
			continue
		}
		if specHasType(s.ComputePlatforms.Discovery.Types, c.Type) {
			computePlatforms = append(computePlatforms, cloudconnectorsv1.CloudConnectorComputePlatformCapability{
				Type:         c.Type,
				Discovery:    c.Discovery,
				Installation: c.ComputePlatforms,
			})
		}
	}
	return workloads, computePlatforms
}

func specHasType(types []spec.Type, typeName string) bool {
	for _, t := range types {
		if t.Name == typeName {
			return true
		}
	}
	return false
}

// secretDataForProvider filters the provided credentials down to the provider
// spec's declared auth field names (the keys the connector runtime references via
// Secret keyRefs), so we never persist stray/unknown values into the Secret.
func secretDataForProvider(provider string, creds map[string]string) map[string]string {
	if len(creds) == 0 {
		return nil
	}
	s, ok := spec.Get(provider)
	if !ok {
		return creds
	}
	allowed := make(map[string]bool)
	for _, f := range s.CredentialFields() {
		allowed[f.Name] = true
	}
	out := make(map[string]string, len(creds))
	for k, v := range creds {
		if allowed[k] {
			out[k] = v
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// cloudConnectorTestResult maps the tester's Result to its GraphQL model,
// normalizing the checks slice so the non-null list never resolves to null.
func cloudConnectorTestResult(res *testconnection.Result) *model.CloudConnectorTestResult {
	out := &model.CloudConnectorTestResult{
		Success:   res.Success,
		AccountID: res.AccountID,
		Identity:  res.Identity,
		Checks:    make([]*model.CloudConnectorTestCheck, 0, len(res.Checks)),
	}
	for _, c := range res.Checks {
		out.Checks = append(out.Checks, &model.CloudConnectorTestCheck{
			Name:        string(c.Name),
			DisplayName: c.DisplayName,
			Passed:      c.Passed,
			Message:     c.Message,
			Remediation: c.Remediation,
		})
	}
	return out
}

func bootstrapRegionValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
