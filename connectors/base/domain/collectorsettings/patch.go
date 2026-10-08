package collectorsettings

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/odigos-io/odigos-enterprise/connectors/base/domain/collectoringress"
)

// Writing the settings tree, as the typed counterpart to Read.
//
// Read is deliberately forgiving — one mistyped value must not stop a connector reconciling anything —
// which is only safe because something REFUSES a bad value at write time, where there is somebody to
// tell. That something is this file. Every field a customer can set through the managed-Collector
// mutations passes through Patch.Validate before it reaches the config tree.
//
// Patch is sparse, and every field is a pointer for one reason: an update must be able to say "leave
// this alone". The slices are pointers-to-slices rather than plain nil-able slices because an explicitly
// EMPTY list is a real instruction — it is the only way to clear an inherited provider list or ingress
// identity list — and a plain nil slice cannot distinguish that from "not mentioned".

// Patch is a sparse set of regional Collector settings to write.
type Patch struct {
	Enabled *bool

	HostNetworkID     *string
	DeploymentRoleRef *string
	// Provider is a sparse, opaque patch merged into the region's provider map. The base layer never
	// interprets these keys; the selected provider owns their names, validation and semantics.
	Provider map[string]any

	CPU              *string
	Memory           *string
	LogRetentionDays *int

	Arch                    *string
	ImageRepositoryOverride *string
	EndpointOverride        *string
	RolloutBatchSize        *int

	Scaling *ScalingPatch
	TLS     *TransportPatch
	Ingress *IngressPatch
}

// ScalingPatch is the replica policy. Mode is required when a scaling block is supplied at all: the
// other fields mean different things under each mode, and applying them without one would be a guess.
type ScalingPatch struct {
	Mode                string
	DesiredCount        *int
	MinCount            *int
	MaxCount            *int
	TargetCPUPercent    *int
	TargetMemoryPercent *int
}

type TransportPatch struct {
	Mode           *string
	Hostname       *string
	CertificateRef *string
}

type IngressPatch struct {
	AdditionalNetworkIdentityIDs *[]string
	AdditionalCIDRs              *[]string
	AllowNetworkCIDRFallback     *bool
	MinFallbackPrefixLength      *int
}

// Architectures a Collector image is published for.
var validArches = []string{"amd64", "arm64"}

// Validate refuses a patch that would produce a Collector nothing can deploy.
//
// It checks what is CHECKABLE here: closed sets, ranges, and pairs that contradict each other. It does
// not check whether a subnet exists or a certificate is issuable — those need the provider, fail later
// with a provider error, and are reported as issues on the Collector rather than as a refused mutation.
func (p Patch) Validate() error {
	if p.DeploymentRoleRef != nil && strings.TrimSpace(*p.DeploymentRoleRef) == "" {
		return fmt.Errorf("deploymentRoleRef must name a customer-provisioned deployment identity")
	}
	if p.CPU != nil && *p.CPU != "" {
		if _, err := strconv.Atoi(strings.TrimSpace(*p.CPU)); err != nil {
			return fmt.Errorf("cpu must be a whole number of CPU units (e.g. \"1024\"), got %q", *p.CPU)
		}
	}
	if p.Memory != nil && *p.Memory != "" {
		// Memory also sizes the Collector's memory_limiter, so an unparseable value is not merely a
		// deployment problem — the renderer cannot size the processor and the Collector would run with a
		// limiter it computed from a guess.
		if _, err := strconv.Atoi(strings.TrimSpace(*p.Memory)); err != nil {
			return fmt.Errorf("memory must be a whole number of MiB (e.g. \"2048\"), got %q", *p.Memory)
		}
	}
	if p.LogRetentionDays != nil && *p.LogRetentionDays < 0 {
		return fmt.Errorf("logRetentionDays cannot be negative")
	}
	if p.RolloutBatchSize != nil && *p.RolloutBatchSize < 0 {
		return fmt.Errorf("rolloutBatchSize cannot be negative")
	}
	if p.Arch != nil && *p.Arch != "" && !contains(validArches, *p.Arch) {
		return fmt.Errorf("arch must be one of %s, got %q", strings.Join(validArches, ", "), *p.Arch)
	}

	if err := p.Scaling.validate(); err != nil {
		return err
	}
	if err := p.TLS.validate(); err != nil {
		return err
	}
	return p.Ingress.validate()
}

func (s *ScalingPatch) validate() error {
	if s == nil {
		return nil
	}
	switch s.Mode {
	case ScalingManual, ScalingAuto:
	default:
		return fmt.Errorf("scaling mode must be %q or %q, got %q", ScalingManual, ScalingAuto, s.Mode)
	}
	for label, v := range map[string]*int{
		"desiredCount": s.DesiredCount, "minCount": s.MinCount, "maxCount": s.MaxCount,
	} {
		if v != nil && *v < 0 {
			return fmt.Errorf("scaling %s cannot be negative", label)
		}
	}
	if s.MinCount != nil && s.MaxCount != nil && *s.MinCount > *s.MaxCount {
		return fmt.Errorf("scaling minCount (%d) cannot exceed maxCount (%d)", *s.MinCount, *s.MaxCount)
	}
	for label, v := range map[string]*int{
		"targetCpuPercent": s.TargetCPUPercent, "targetMemoryPercent": s.TargetMemoryPercent,
	} {
		if v != nil && (*v <= 0 || *v > 100) {
			return fmt.Errorf("scaling %s must be between 1 and 100, got %d", label, *v)
		}
	}
	return nil
}

func (t *TransportPatch) validate() error {
	if t == nil {
		return nil
	}
	if t.Mode == nil || *t.Mode == "" {
		return nil
	}
	mode := collectoringress.TLSMode(*t.Mode)
	switch mode {
	case collectoringress.TLSOff, collectoringress.TLSPlatform,
		collectoringress.TLSLoadBalancer, collectoringress.TLSCollector:
	default:
		return fmt.Errorf("transport mode %q is not one this Collector can serve", *t.Mode)
	}
	// A mode that presents a certificate needs a hostname the customer owns: a load balancer's own
	// generated DNS name is not issuable by any CA, so a certificate configured against it fails
	// verification on every export. Refused here rather than attempted and reported later.
	if mode == collectoringress.TLSLoadBalancer || mode == collectoringress.TLSCollector {
		if t.Hostname == nil || strings.TrimSpace(*t.Hostname) == "" {
			return fmt.Errorf("transport mode %q needs a hostname you own, so the certificate can be verified", *t.Mode)
		}
	}
	return nil
}

func (i *IngressPatch) validate() error {
	if i == nil {
		return nil
	}
	if i.MinFallbackPrefixLength != nil {
		// Zero means provider default. The shared contract cannot impose an IPv4 /32 ceiling: providers
		// own their address-family and safe-fallback constraints.
		if *i.MinFallbackPrefixLength < 0 {
			return fmt.Errorf("ingress minFallbackPrefixLength cannot be negative, got %d", *i.MinFallbackPrefixLength)
		}
	}
	return nil
}

// MergeInto applies the patch over one region's existing config entry, returning the new entry.
//
// The entry is never mutated in place: the caller holds a copy of the effective config, and writing
// through it would change what a concurrent reader sees before the write is persisted.
func (p Patch) MergeInto(entry map[string]any) map[string]any {
	out := copyEntry(entry)

	setBool(out, KeyEnabled, p.Enabled)
	setString(out, KeyHostNetworkID, p.HostNetworkID)
	setString(out, KeyDeploymentRoleRef, p.DeploymentRoleRef)
	if len(p.Provider) > 0 {
		provider := copyEntry(mapOf(out, KeyProvider))
		for key, value := range p.Provider {
			provider[key] = value
		}
		out[KeyProvider] = provider
	}
	setString(out, KeyCPU, p.CPU)
	setString(out, KeyMemory, p.Memory)
	setInt(out, KeyLogRetentionDays, p.LogRetentionDays)
	setString(out, KeyArch, p.Arch)
	setString(out, KeyEndpointOverride, p.EndpointOverride)
	setInt(out, KeyRolloutBatchSize, p.RolloutBatchSize)

	if p.ImageRepositoryOverride != nil {
		image := copyEntry(mapOf(out, KeyImage))
		image[KeyRepoOverride] = *p.ImageRepositoryOverride
		out[KeyImage] = image
	}
	if s := p.Scaling; s != nil {
		scaling := copyEntry(mapOf(out, KeyScaling))
		scaling[KeyScalingMode] = s.Mode
		setInt(scaling, KeyDesiredCount, s.DesiredCount)
		setInt(scaling, KeyMinCount, s.MinCount)
		setInt(scaling, KeyMaxCount, s.MaxCount)
		setInt(scaling, KeyTargetCPUPercent, s.TargetCPUPercent)
		setInt(scaling, KeyTargetMemoryPercent, s.TargetMemoryPercent)
		out[KeyScaling] = scaling
	}
	if t := p.TLS; t != nil {
		tls := copyEntry(mapOf(out, KeyTLS))
		setString(tls, KeyTLSMode, t.Mode)
		setString(tls, KeyTLSHostname, t.Hostname)
		setString(tls, KeyTLSCertificateRef, t.CertificateRef)
		out[KeyTLS] = tls
	}
	if i := p.Ingress; i != nil {
		ingress := copyEntry(mapOf(out, KeyIngress))
		setStrings(ingress, KeyAdditionalNetworkIdentityIDs, i.AdditionalNetworkIdentityIDs)
		setStrings(ingress, KeyAdditionalCIDRs, i.AdditionalCIDRs)
		setBool(ingress, KeyAllowNetworkCIDRFallback, i.AllowNetworkCIDRFallback)
		setInt(ingress, KeyMinFallbackPrefixLength, i.MinFallbackPrefixLength)
		out[KeyIngress] = ingress
	}
	return out
}

// WriteRegion returns a new `collector` tree with one region's entry merged in.
//
// It also turns the feature switch on when it writes an enabled region, because the switch gates every
// region: creating a Collector through the API and having nothing happen — because a top-level flag
// nobody mentioned is still false — is not a defensible outcome of an explicit create.
func WriteRegion(tree map[string]any, region string, p Patch) (map[string]any, error) {
	if strings.TrimSpace(region) == "" {
		return nil, fmt.Errorf("a Collector needs a region")
	}
	if err := p.Validate(); err != nil {
		return nil, err
	}

	out := copyEntry(tree)
	regions := copyEntry(mapOf(out, KeyRegions))
	regions[region] = p.MergeInto(mapOf(regions, region))
	out[KeyRegions] = regions

	if enabled := boolOf(regions[region].(map[string]any), KeyEnabled, false); enabled {
		out[KeyEnabled] = true
	}
	return out, nil
}

// RemoveRegion returns a new tree with one region's entry gone.
//
// Removal rather than `enabled: false`, because this backs a deliberate destroy: a disabled entry left
// behind would be silently re-applied — with whatever sizes and overrides it carried — to a Collector
// created in that region months later, by somebody who never saw it.
func RemoveRegion(tree map[string]any, region string) map[string]any {
	out := copyEntry(tree)
	regions := copyEntry(mapOf(out, KeyRegions))
	delete(regions, region)
	out[KeyRegions] = regions
	return out
}

// HasRegion reports whether the tree carries an explicit entry for a region.
//
// Explicit is the operative word: Settings.Region resolves an unconfigured region from the defaults, so
// a reader always gets settings back. The question here is whether somebody CONFIGURED this region,
// which is what separates a create from an update.
func HasRegion(tree map[string]any, region string) bool {
	_, ok := mapOf(tree, KeyRegions)[region]
	return ok
}

// ConfiguredRegions lists the explicitly configured regions, in stable order.
func ConfiguredRegions(tree map[string]any) []string {
	regions := mapOf(tree, KeyRegions)
	out := make([]string, 0, len(regions))
	for id := range regions {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

func copyEntry(m map[string]any) map[string]any {
	out := make(map[string]any, len(m)+1)
	for k, v := range m {
		out[k] = v
	}
	return out
}

func setString(m map[string]any, key string, v *string) {
	if v != nil {
		m[key] = *v
	}
}

func setBool(m map[string]any, key string, v *bool) {
	if v != nil {
		m[key] = *v
	}
}

func setInt(m map[string]any, key string, v *int) {
	if v != nil {
		m[key] = *v
	}
}

// setStrings writes a list, preserving the empty-means-none semantics: the caller supplying an empty
// slice through a non-nil pointer is clearing the inherited default, which is a different instruction
// from not mentioning the field.
func setStrings(m map[string]any, key string, v *[]string) {
	if v == nil {
		return
	}
	items := make([]any, 0, len(*v))
	for _, s := range *v {
		items = append(items, s)
	}
	m[key] = items
}

func contains(list []string, v string) bool {
	for _, item := range list {
		if item == v {
			return true
		}
	}
	return false
}
