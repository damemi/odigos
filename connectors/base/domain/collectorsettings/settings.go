// Package collectorsettings reads the managed-Collector operational configuration: the
// `collector.enabled` / `collector.defaults.*` / `collector.regions.<region>.*` tree from
//
// Pure — no I/O. It takes the effective config map the DB-backed config.Manager already serves and
// resolves it into typed regional settings, so every input to a Collector is tunable live through
// `updateConfig` over agents-api: no pod restart, no CRD change, no UI change.
//
// Precedence is defaults, then the per-region override. The legacy networks map remains readable for
// older configuration, but it no longer defines Collector identity or creates additional Collectors.
package collectorsettings

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/odigos-io/odigos-enterprise/connectors/base/domain/collectoringress"
)

// Key is the single top-level operational config key holding the whole tree.
//
// One key rather than a flat `collector.regions.eu-west-1.cpu` namespace because config.Manager rejects
// unknown top-level keys, and region keys cannot be declared in advance. The cost is that a patch
// replaces this subtree wholesale, the same as `discoveryTags`.
const Key = "collector"

// Config tree keys. Named rather than inlined so a typo is a compile error and so Defaults, the reader
// and the tests cannot disagree about a spelling.
const (
	KeyEnabled  = "enabled"
	KeyDefaults = "defaults"
	// KeyRegions contains the logical regional Collector settings. A region is the deployment unit.
	KeyRegions = "regions"

	KeyHostNetworkID = "hostNetworkId"
	// KeyProvider holds provider-owned settings. The base layer preserves and merges this opaque map;
	// the selected provider is solely responsible for interpreting it.
	KeyProvider         = "provider"
	KeyCPU              = "cpu"
	KeyMemory           = "memory"
	KeyLogRetentionDays = "logRetentionDays"
	KeyArch             = "arch"
	KeyImage            = "image"
	KeyRepoOverride     = "repositoryOverride"
	KeyEndpointOverride = "endpointOverride"

	KeyRetention                 = "retention"
	KeyIdleScaleToZeroAfterHours = "idleScaleToZeroAfterHours"
	KeyIdleDeleteAfterHours      = "idleDeleteAfterHours"

	KeyScaling             = "scaling"
	KeyScalingMode         = "mode"
	KeyDesiredCount        = "desiredCount"
	KeyMinCount            = "minCount"
	KeyMaxCount            = "maxCount"
	KeyTargetCPUPercent    = "targetCpuPercent"
	KeyTargetMemoryPercent = "targetMemoryPercent"

	KeyTLS               = "tls"
	KeyTLSMode           = "mode"
	KeyTLSHostname       = "hostname"
	KeyTLSCertificateRef = "certificateRef"

	KeyIngress                      = "ingress"
	KeyAdditionalNetworkIdentityIDs = "additionalNetworkIdentityIds"
	KeyAdditionalCIDRs              = "additionalCidrs"
	KeyAllowNetworkCIDRFallback     = "allowNetworkCidrFallback"
	KeyMinFallbackPrefixLength      = "minFallbackPrefixLength"
)

// Scaling modes.
const (
	ScalingManual = "manual"
	ScalingAuto   = "auto"
)

// Defaults for a Collector nobody has configured. The "default" size.
const (
	DefaultCPU              = "1024"
	DefaultMemory           = "2048"
	DefaultLogRetentionDays = 7
	DefaultDesiredCount     = 2
)

// Defaults returns the code defaults for the whole tree, for config.MergeDefaults.
//
// `enabled` is false. Turning managed Collectors on is a rollout — every instrumented service on the
// network gets a new task-definition revision — so it is never the default.
func Defaults() map[string]any {
	return map[string]any{
		Key: map[string]any{
			KeyEnabled: false,
			KeyDefaults: map[string]any{
				KeyCPU:              DefaultCPU,
				KeyMemory:           DefaultMemory,
				KeyLogRetentionDays: DefaultLogRetentionDays,
				KeyScaling: map[string]any{
					KeyScalingMode:  ScalingManual,
					KeyDesiredCount: DefaultDesiredCount,
				},
			},
			KeyRegions: map[string]any{},
		},
	}
}

// Scaling is a Collector's replica policy.
type Scaling struct {
	// Mode is "manual" or "auto". Manual holds a fixed count; auto hands the platform's own autoscaler
	// the bounds and targets below.
	Mode string
	// DesiredCount applies in manual mode.
	DesiredCount int
	// MinCount / MaxCount / Target*Percent apply in auto mode.
	MinCount            int
	MaxCount            int
	TargetCPUPercent    int
	TargetMemoryPercent int
}

// Network holds portable operational settings shared by defaults and a regional override.
type Network struct {
	// Enabled is inherited by a Region. It is not a workload-network switch.
	Enabled bool

	// Provider is opaque provider-owned configuration. Keeping this map opaque prevents the shared
	// contract from acquiring AWS subnet, security-group, cluster, or launch-type semantics.
	Provider map[string]any

	// Size. Memory also sizes memory_limiter, which is why a size change is a configuration push as
	// well as a workload change.
	CPU              string
	Memory           string
	LogRetentionDays int

	Scaling Scaling
	// Retention is what happens to this Collector when its network has no enabled sources left.
	// Both halves default to keeping it, which is why nothing about it appears in Defaults().
	Retention Retention

	// Arch and ImageRepositoryOverride select the image. The digest is unaffected by either.
	Arch                    string
	ImageRepositoryOverride string

	// EndpointOverride replaces the Odigos-created load balancer: when set, Odigos creates none and
	// publishes what the customer pointed at.
	EndpointOverride string
	TLS              collectoringress.TransportConfig
	Ingress          collectoringress.Config
}

// Region is one regional Collector's resolved settings. HostNetworkID is only the physical VPC in
// which it runs; consumer reachability is verified by the provider and is not represented as a graph.
type Region struct {
	Network
	Region        string
	HostNetworkID string
}

// Deploys reports whether this region is eligible for a Collector. The actual
// source/host candidate decision belongs to collectorplacement.
func (r Region) Deploys() bool { return r.Enabled }

// Settings is the resolved feature configuration.
type Settings struct {
	// Enabled is the feature switch. False means no Collector anywhere, whatever a network says.
	Enabled bool
	// Defaults is the resolved default regional configuration.
	Defaults Network
	// Regions holds the region-keyed Collector settings. A missing region inherits
	// Defaults and the feature switch, just as a missing legacy network entry does.
	Regions map[string]Region
}

// Region resolves one region's settings, falling back to the regional defaults.
func (s Settings) Region(id string) Region {
	if r, ok := s.Regions[id]; ok {
		return r
	}
	r := Region{Network: s.Defaults, Region: id}
	r.Enabled = s.Enabled
	return r
}

// DeployRegions returns explicitly configured enabled regions in stable order.
// These entries are the Collector lifecycle intent: discovery supplies physical networks and sources
// supply consumers, but neither is allowed to create a Collector in an unconfigured region.
func (s Settings) DeployRegions() []string {
	if !s.Enabled {
		return nil
	}
	ids := make([]string, 0, len(s.Regions))
	for id, r := range s.Regions {
		if r.Deploys() {
			ids = append(ids, id)
		}
	}
	sortStrings(ids)
	return ids
}

// Read resolves the tree out of the effective operational config.
//
// Never fails: a malformed value falls back to the default rather than refusing the whole
// configuration. config.Manager validates on write, so a bad value here means the tree was written
// before this reader understood it — and refusing at read time would stop a connector from reconciling
// anything, over one mistyped field on one network.
func Read(effective map[string]any) Settings {
	tree := mapOf(effective, Key)

	enabled := boolOf(tree, KeyEnabled, false)
	defaults := readNetwork(mapOf(tree, KeyDefaults), Network{
		Enabled:          enabled,
		CPU:              DefaultCPU,
		Memory:           DefaultMemory,
		LogRetentionDays: DefaultLogRetentionDays,
		Scaling:          Scaling{Mode: ScalingManual, DesiredCount: DefaultDesiredCount},
		Ingress:          collectoringress.Config{AllowNetworkCIDRFallback: true},
	})

	regions := map[string]Region{}
	for id, raw := range mapOf(tree, KeyRegions) {
		entry, ok := raw.(map[string]any)
		if !ok || id == "" {
			continue
		}
		region := readRegion(entry, id, defaults)
		if !enabled {
			region.Enabled = false
		}
		regions[id] = region
	}

	return Settings{Enabled: enabled, Defaults: defaults, Regions: regions}
}

func readRegion(entry map[string]any, id string, base Network) Region {
	n := readNetwork(entry, base)
	return Region{Network: n, Region: id, HostNetworkID: stringOf(entry, KeyHostNetworkID, "")}
}

// readNetwork applies one entry's overrides over a base. Every field falls through to the base when the
// entry does not mention it, which is the whole of the precedence rule.
func readNetwork(entry map[string]any, base Network) Network {
	out := base
	out.Enabled = boolOf(entry, KeyEnabled, base.Enabled)
	out.Provider = mergeMaps(base.Provider, mapOf(entry, KeyProvider))
	out.CPU = stringOf(entry, KeyCPU, base.CPU)
	out.Memory = stringOf(entry, KeyMemory, base.Memory)
	out.LogRetentionDays = intOf(entry, KeyLogRetentionDays, base.LogRetentionDays)
	out.Arch = stringOf(entry, KeyArch, base.Arch)
	out.ImageRepositoryOverride = stringOf(mapOf(entry, KeyImage), KeyRepoOverride, base.ImageRepositoryOverride)
	out.EndpointOverride = stringOf(entry, KeyEndpointOverride, base.EndpointOverride)

	scaling := mapOf(entry, KeyScaling)
	out.Scaling = Scaling{
		Mode:                stringOf(scaling, KeyScalingMode, base.Scaling.Mode),
		DesiredCount:        intOf(scaling, KeyDesiredCount, base.Scaling.DesiredCount),
		MinCount:            intOf(scaling, KeyMinCount, base.Scaling.MinCount),
		MaxCount:            intOf(scaling, KeyMaxCount, base.Scaling.MaxCount),
		TargetCPUPercent:    intOf(scaling, KeyTargetCPUPercent, base.Scaling.TargetCPUPercent),
		TargetMemoryPercent: intOf(scaling, KeyTargetMemoryPercent, base.Scaling.TargetMemoryPercent),
	}

	retention := mapOf(entry, KeyRetention)
	out.Retention = Retention{
		IdleScaleToZeroAfterHours: intOf(retention, KeyIdleScaleToZeroAfterHours, base.Retention.IdleScaleToZeroAfterHours),
		IdleDeleteAfterHours:      intOf(retention, KeyIdleDeleteAfterHours, base.Retention.IdleDeleteAfterHours),
	}

	tls := mapOf(entry, KeyTLS)
	out.TLS = collectoringress.TransportConfig{
		Mode:           collectoringress.TLSMode(stringOf(tls, KeyTLSMode, string(base.TLS.Mode))),
		Hostname:       stringOf(tls, KeyTLSHostname, base.TLS.Hostname),
		CertificateRef: stringOf(tls, KeyTLSCertificateRef, base.TLS.CertificateRef),
		// The transport resolver reads the override's scheme, so it travels with the TLS config rather
		// than being looked up separately — the endpoint and what protects it are one decision.
		EndpointOverride: out.EndpointOverride,
	}

	ingress := mapOf(entry, KeyIngress)
	out.Ingress = collectoringress.Config{
		AdditionalNetworkIdentityIDs: stringsOf(ingress, KeyAdditionalNetworkIdentityIDs, base.Ingress.AdditionalNetworkIdentityIDs),
		AdditionalCIDRs:              stringsOf(ingress, KeyAdditionalCIDRs, base.Ingress.AdditionalCIDRs),
		AllowNetworkCIDRFallback:     boolOf(ingress, KeyAllowNetworkCIDRFallback, base.Ingress.AllowNetworkCIDRFallback),
		MinFallbackPrefixLength:      intOf(ingress, KeyMinFallbackPrefixLength, base.Ingress.MinFallbackPrefixLength),
	}
	return out
}

func mergeMaps(base, override map[string]any) map[string]any {
	out := make(map[string]any, len(base)+len(override))
	for key, value := range base {
		out[key] = value
	}
	for key, value := range override {
		if child, ok := value.(map[string]any); ok {
			out[key] = mergeMaps(mapValue(out[key]), child)
			continue
		}
		out[key] = value
	}
	return out
}

func mapValue(value any) map[string]any {
	if m, ok := value.(map[string]any); ok {
		return m
	}
	return nil
}

// MemoryMiB parses the memory allocation for memory_limiter sizing. Reports whether it parsed, so a
// caller can refuse rather than render a Collector around a guess.
func (n Network) MemoryMiB() (int, error) {
	v := strings.TrimSpace(n.Memory)
	if v == "" {
		return 0, fmt.Errorf("no memory allocation is configured for this Collector")
	}
	mib, err := strconv.Atoi(v)
	if err != nil {
		return 0, fmt.Errorf("memory %q is not a whole number of MiB", n.Memory)
	}
	return mib, nil
}

func mapOf(m map[string]any, key string) map[string]any {
	if v, ok := m[key].(map[string]any); ok {
		return v
	}
	return nil
}

func boolOf(m map[string]any, key string, def bool) bool {
	if v, ok := m[key].(bool); ok {
		return v
	}
	return def
}

func stringOf(m map[string]any, key, def string) string {
	if v, ok := m[key].(string); ok && v != "" {
		return v
	}
	return def
}

// intOf accepts float64 as well as int, because a value that arrived as JSON — which every
// updateConfig patch does — decodes numbers as float64.
func intOf(m map[string]any, key string, def int) int {
	switch v := m[key].(type) {
	case int:
		return v
	case int64:
		return int(v)
	case float64:
		return int(v)
	}
	return def
}

func stringsOf(m map[string]any, key string, def []string) []string {
	raw, ok := m[key].([]any)
	if !ok {
		if v, isStrings := m[key].([]string); isStrings {
			return v
		}
		return def
	}
	// An explicitly empty list means "none", not "inherit". Clearing an inherited list is the only way
	// to say a network wants none of the defaults' subnets or ingress rules.
	out := make([]string, 0, len(raw))
	for _, item := range raw {
		if s, isStr := item.(string); isStr && s != "" {
			out = append(out, s)
		}
	}
	return out
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}
