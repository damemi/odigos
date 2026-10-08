// Package collectorsettings interprets the AWS-owned portion of the provider-neutral Collector
// configuration. The base package preserves provider values as opaque data; this package gives those
// values AWS meaning.
package collectorsettings

import base "github.com/odigos-io/odigos-enterprise/connectors/base/domain/collectorsettings"
import "github.com/odigos-io/odigos-enterprise/connectors/base/domain/collectoringress"

const (
	Key              = base.Key
	KeyEnabled       = base.KeyEnabled
	KeyDefaults      = base.KeyDefaults
	KeyRegions       = base.KeyRegions
	KeyHostNetworkID = base.KeyHostNetworkID
	KeyProvider      = base.KeyProvider

	KeySubnetIDs        = "subnetIds"
	KeySecurityGroupIDs = "securityGroupIds"
	KeyClusterRef       = "clusterArn"
	KeyExecutionRoleARN = "executionRoleArn"
	KeyAssignPublicIP   = "assignPublicIp"
	KeyUseFargateSpot   = "useFargateSpot"
)

const (
	ScalingManual       = base.ScalingManual
	ScalingAuto         = base.ScalingAuto
	DefaultCPU          = base.DefaultCPU
	DefaultMemory       = base.DefaultMemory
	DefaultDesiredCount = base.DefaultDesiredCount
	DefaultMinCount     = base.DefaultMinCount
	DefaultMaxCount     = base.DefaultMaxCount
	ReplicaFloor        = base.ReplicaFloor
)

type (
	Scaling           = base.Scaling
	ResolvedScaling   = base.ResolvedScaling
	Retention         = base.Retention
	ResolvedRetention = base.ResolvedRetention
	RetentionAction   = base.RetentionAction
)

const (
	RetentionKeep        = base.RetentionKeep
	RetentionScaleToZero = base.RetentionScaleToZero
	RetentionDelete      = base.RetentionDelete
)

type Network struct {
	Enabled                 bool
	Provider                map[string]any
	CPU                     string
	Memory                  string
	LogRetentionDays        int
	Scaling                 Scaling
	RolloutBatchSize        int
	Retention               Retention
	Arch                    string
	ImageRepositoryOverride string
	EndpointOverride        string
	TLS                     collectoringress.TransportConfig
	Ingress                 collectoringress.Config

	SubnetIDs        []string
	SecurityGroupIDs []string
	ClusterRef       string
	ExecutionRoleARN string
	AssignPublicIP   bool
	UseFargateSpot   bool
}

func (n Network) Base() base.Network {
	return base.Network{
		Enabled: n.Enabled, Provider: n.Provider, CPU: n.CPU, Memory: n.Memory,
		LogRetentionDays: n.LogRetentionDays, Scaling: n.Scaling,
		RolloutBatchSize: n.RolloutBatchSize, Retention: n.Retention,
		Arch: n.Arch, ImageRepositoryOverride: n.ImageRepositoryOverride,
		EndpointOverride: n.EndpointOverride, TLS: n.TLS, Ingress: n.Ingress,
	}
}

func (n Network) MemoryMiB() (int, error) { return n.Base().MemoryMiB() }

type Region struct {
	Network
	Region        string
	HostNetworkID string
}

func (r Region) Deploys() bool { return r.Enabled }

type Settings struct {
	Enabled  bool
	Defaults Network
	Regions  map[string]Region
}

func Read(effective map[string]any) Settings { return FromBase(base.Read(effective)) }

func FromBase(in base.Settings) Settings {
	out := Settings{Enabled: in.Enabled, Defaults: network(in.Defaults), Regions: map[string]Region{}}
	for id, region := range in.Regions {
		out.Regions[id] = Region{
			Network:       network(region.Network),
			Region:        region.Region,
			HostNetworkID: region.HostNetworkID,
		}
	}
	return out
}

// Base projects AWS settings back to the provider-neutral lifecycle and placement contract.
func (s Settings) Base() base.Settings {
	regions := make(map[string]base.Region, len(s.Regions))
	for id, region := range s.Regions {
		regions[id] = base.Region{
			Network:       region.Network.Base(),
			Region:        region.Region,
			HostNetworkID: region.HostNetworkID,
		}
	}
	return base.Settings{Enabled: s.Enabled, Defaults: s.Defaults.Base(), Regions: regions}
}

func (s Settings) Region(id string) Region {
	if region, ok := s.Regions[id]; ok {
		return region
	}
	region := Region{Network: s.Defaults, Region: id}
	region.Enabled = s.Enabled
	return region
}

func (s Settings) DeployRegions() []string {
	if !s.Enabled {
		return nil
	}
	ids := make([]string, 0, len(s.Regions))
	for id, region := range s.Regions {
		if region.Deploys() {
			ids = append(ids, id)
		}
	}
	for i := 1; i < len(ids); i++ {
		for j := i; j > 0 && ids[j] < ids[j-1]; j-- {
			ids[j], ids[j-1] = ids[j-1], ids[j]
		}
	}
	return ids
}

func network(in base.Network) Network {
	provider := in.Provider
	return Network{
		Enabled:                 in.Enabled,
		Provider:                in.Provider,
		CPU:                     in.CPU,
		Memory:                  in.Memory,
		LogRetentionDays:        in.LogRetentionDays,
		Scaling:                 in.Scaling,
		RolloutBatchSize:        in.RolloutBatchSize,
		Retention:               in.Retention,
		Arch:                    in.Arch,
		ImageRepositoryOverride: in.ImageRepositoryOverride,
		EndpointOverride:        in.EndpointOverride,
		TLS:                     in.TLS,
		Ingress:                 in.Ingress,
		SubnetIDs:               stringsOf(provider, KeySubnetIDs),
		SecurityGroupIDs:        stringsOf(provider, KeySecurityGroupIDs),
		ClusterRef:              stringOf(provider, KeyClusterRef),
		ExecutionRoleARN:        stringOf(provider, KeyExecutionRoleARN),
		AssignPublicIP:          boolOf(provider, KeyAssignPublicIP),
		UseFargateSpot:          boolOf(provider, KeyUseFargateSpot),
	}
}

func stringsOf(values map[string]any, key string) []string {
	raw, ok := values[key].([]any)
	if !ok {
		if strings, ok := values[key].([]string); ok {
			return strings
		}
		return nil
	}
	out := make([]string, 0, len(raw))
	for _, value := range raw {
		if text, ok := value.(string); ok && text != "" {
			out = append(out, text)
		}
	}
	return out
}

func stringOf(values map[string]any, key string) string {
	if value, ok := values[key].(string); ok {
		return value
	}
	return ""
}

func boolOf(values map[string]any, key string) bool {
	value, _ := values[key].(bool)
	return value
}
