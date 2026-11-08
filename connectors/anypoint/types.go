package anypoint

import (
	"strconv"
	"strings"
)

// Identity is who the Connected App is: the organization (business group) its credentials belong to.
type Identity struct {
	OrgID      string
	ClientName string
}

// Environment is an Anypoint environment (Production, Sandbox, ...).
type Environment struct {
	ID             string `json:"id"`
	Name           string `json:"name"`
	OrganizationID string `json:"organizationId"`
	Type           string `json:"type"`
	IsProduction   bool   `json:"isProduction"`
}

// Target type classes, normalized from whatever spelling the targets API reports.
const (
	TargetSharedSpace  = "shared-space"
	TargetPrivateSpace = "private-space"
	TargetRuntimeFab   = "runtime-fabric"
	TargetUnknown      = "unknown"
)

// Target is a deployment target: a CloudHub 2.0 shared or private space, or a Runtime Fabric.
type Target struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Type   string `json:"type"`
	Region string `json:"region"`
	Status string `json:"status"`
}

// Class normalizes the target's type to one of the Target* constants. The targets API spells types in more
// than one way across target generations (unverified), so this matches on substrings rather than equality.
func (t Target) Class() string {
	s := strings.ToLower(t.Type)
	switch {
	case strings.Contains(s, "shared"):
		return TargetSharedSpace
	case strings.Contains(s, "private"):
		return TargetPrivateSpace
	case strings.Contains(s, "fabric"), strings.Contains(s, "rtf"):
		return TargetRuntimeFab
	}
	return TargetUnknown
}

// PropertiesServiceKey is the application-configuration entry that carries a Mule app's properties. It is the
// only configuration entry the connector reads or writes.
const PropertiesServiceKey = "mule.agent.application.properties.service"

// PropertiesService is a Mule app's application properties as Runtime Manager stores them. Secure property
// VALUES are write-only on the platform: a read returns the keys with masked or empty values, so callers must
// treat SecureProperties as a key set and never as data to compare or restore.
type PropertiesService struct {
	ApplicationName  string            `json:"applicationName,omitempty"`
	Properties       map[string]string `json:"properties"`
	SecureProperties map[string]string `json:"secureProperties,omitempty"`
}

// ApplicationConfiguration is the configuration block of an AMC deployment's application.
type ApplicationConfiguration struct {
	PropertiesService *PropertiesService `json:"mule.agent.application.properties.service,omitempty"`
}

// DeploymentApplication is the application half of an AMC deployment.
type DeploymentApplication struct {
	// Status is the runtime status, e.g. RUNNING, NOT_RUNNING.
	Status string `json:"status,omitempty"`
	// DesiredState is STARTED or STOPPED.
	DesiredState  string                    `json:"desiredState,omitempty"`
	Configuration *ApplicationConfiguration `json:"configuration,omitempty"`
}

// DeploymentSettings is the subset of a deployment's target settings the connector reads.
type DeploymentSettings struct {
	RuntimeVersion string `json:"runtimeVersion,omitempty"`
}

// DeploymentTarget is the target half of an AMC deployment.
type DeploymentTarget struct {
	Provider           string              `json:"provider,omitempty"`
	TargetID           string              `json:"targetId,omitempty"`
	Replicas           int                 `json:"replicas,omitempty"`
	DeploymentSettings *DeploymentSettings `json:"deploymentSettings,omitempty"`
}

// Deployment statuses the connector acts on. Others (e.g. APPLYING, DEPLOYING) mean an update is in flight.
const (
	DeploymentApplied = "APPLIED"
	DeploymentFailed  = "FAILED"
)

// Deployment is an AMC deployment: one Mule application on a CloudHub 2.0 space or a Runtime Fabric.
type Deployment struct {
	ID                    string                `json:"id"`
	Name                  string                `json:"name"`
	Status                string                `json:"status,omitempty"`
	CurrentRuntimeVersion string                `json:"currentRuntimeVersion,omitempty"`
	Application           DeploymentApplication `json:"application"`
	Target                DeploymentTarget      `json:"target"`
}

// RuntimeVersion is the Mule runtime the deployment runs, preferring the version the platform reports as
// current over the one requested in the deployment settings.
func (d Deployment) RuntimeVersion() string {
	if d.CurrentRuntimeVersion != "" {
		return d.CurrentRuntimeVersion
	}
	if d.Target.DeploymentSettings != nil {
		return d.Target.DeploymentSettings.RuntimeVersion
	}
	return ""
}

// Properties returns the deployment's application properties (never nil).
func (d Deployment) Properties() map[string]string {
	if ps := d.propertiesService(); ps != nil && ps.Properties != nil {
		return ps.Properties
	}
	return map[string]string{}
}

// SecurePropertyKeys returns the names of the deployment's secure properties (values are write-only).
func (d Deployment) SecurePropertyKeys() []string {
	ps := d.propertiesService()
	if ps == nil {
		return nil
	}
	keys := make([]string, 0, len(ps.SecureProperties))
	for k := range ps.SecureProperties {
		keys = append(keys, k)
	}
	return keys
}

func (d Deployment) propertiesService() *PropertiesService {
	if d.Application.Configuration == nil {
		return nil
	}
	return d.Application.Configuration.PropertiesService
}

// HybridTarget is the server, server group or cluster a hybrid application runs on.
type HybridTarget struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
	Type string `json:"type"`
}

// HybridApplication is an application deployed to a customer-managed server through Runtime Manager.
type HybridApplication struct {
	ID                 int          `json:"id"`
	Name               string       `json:"name"`
	DesiredStatus      string       `json:"desiredStatus,omitempty"`
	LastReportedStatus string       `json:"lastReportedStatus,omitempty"`
	Target             HybridTarget `json:"target"`
}

// HybridServer is a Runtime Manager-registered server running a Mule runtime.
type HybridServer struct {
	ID          int    `json:"id"`
	Name        string `json:"name"`
	Status      string `json:"status,omitempty"`
	MuleVersion string `json:"muleVersion,omitempty"`
}

// MuleVersion is a parsed Mule runtime version.
type MuleVersion struct {
	Major, Minor, Patch int
	// Raw is the version as the platform reported it, minus any build/java suffix ("4.11.0").
	Raw string
	// Java is the Java release the runtime image targets when the platform encodes it ("17"), else "".
	Java string
}

// AtLeast reports whether v is the given major.minor or newer.
func (v MuleVersion) AtLeast(major, minor int) bool {
	if v.Major != major {
		return v.Major > major
	}
	return v.Minor >= minor
}

// ParseMuleVersion parses the version strings the platform reports: CloudHub 2.0 and RTF encode the runtime
// image as "4.11.0:1e-java17" (version, then a build and Java tag); hybrid servers report a plain "4.11.0".
// ok is false when no major.minor can be read.
func ParseMuleVersion(s string) (MuleVersion, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return MuleVersion{}, false
	}
	version, tag, _ := strings.Cut(s, ":")
	// Some reports carry an edition or build suffix on the version itself ("4.11.0-e", "4.11.0.2025...").
	version, _, _ = strings.Cut(version, "-")
	parts := strings.Split(version, ".")
	if len(parts) < 2 {
		return MuleVersion{}, false
	}
	nums := make([]int, 3)
	for i := 0; i < 3 && i < len(parts); i++ {
		n, err := strconv.Atoi(parts[i])
		if err != nil {
			if i < 2 {
				return MuleVersion{}, false
			}
			break
		}
		nums[i] = n
	}
	v := MuleVersion{Major: nums[0], Minor: nums[1], Patch: nums[2]}
	v.Raw = strconv.Itoa(v.Major) + "." + strconv.Itoa(v.Minor) + "." + strconv.Itoa(v.Patch)
	if i := strings.Index(tag, "java"); i >= 0 {
		v.Java = strings.TrimLeft(tag[i+len("java"):], "-")
	}
	return v, true
}
