package deployer

import (
	"context"
	"fmt"

	"github.com/odigos-io/odigos-enterprise/connectors/base/domain/collectorconfig"
	"github.com/odigos-io/odigos-enterprise/connectors/base/domain/collectorcredential"
	"github.com/odigos-io/odigos-enterprise/connectors/base/domain/collectorimage"
	"github.com/odigos-io/odigos-enterprise/connectors/base/domain/collectoringress"
	"github.com/odigos-io/odigos-enterprise/connectors/base/domain/collectorownership"
	basecollectorsettings "github.com/odigos-io/odigos-enterprise/connectors/base/domain/collectorsettings"
	"github.com/odigos-io/odigos-enterprise/connectors/base/domain/collectorstatus"
	basestore "github.com/odigos-io/odigos-enterprise/connectors/base/store"
	awsingress "github.com/odigos-io/odigos-enterprise/connectors/providers/aws/pkg/collector/ingress"
	collectorsettings "github.com/odigos-io/odigos-enterprise/connectors/providers/aws/pkg/collector/settings"
)

// Clients supplies the AWS clients this package needs, per region where that matters.
type Clients interface {
	Logs(ctx context.Context, region string) (LogsClient, error)
	Secrets(ctx context.Context, region string) (SecretsClient, error)
	EC2(ctx context.Context, region string) (SecurityGroupClient, error)
	LoadBalancers(ctx context.Context, region string) (LoadBalancerClient, error)
	// ECS serves both the cluster and the task definition, because they are one API and one client.
	ECS(ctx context.Context, region string) (ECSClient, error)
	// AutoScaling is Application Auto Scaling, which is its own API rather than part of ECS — a scalable
	// target lives in the autoscaling service and only points at the ECS service by a composed id.
	AutoScaling(ctx context.Context, region string) (AutoScalingClient, error)
}

// ECSClient is the cluster, task-definition and service surface together. One interface rather than
// three, because one *ecs.Client satisfies all of them and splitting it would make the fake in the tests
// three objects that have to agree about the same cluster.
type ECSClient interface {
	ClusterClient
	TaskDefinitionClient
	ServiceClient
}

// Provisioner provisions Collector identity, logging and credentials for one connector.
type Provisioner struct {
	// Identity is this connector, as collectorownership reads it: the name is the adoption key and the
	// CR UID is the authority key.
	Identity collectorownership.Identity
	Provider string
	// AccountID is the AWS account boundary. Every policy resource is scoped to it, and it is half of
	// every credential binding.
	AccountID string
	// License is the connector's own ODIGOS_ONPREM_TOKEN, wrapped so it cannot reach a log or a
	// serialized structure. the licence installed into a customer's account is THEIRS — the
	// connector copies the value every other enterprise component in that install already uses, and
	// Odigos-the-vendor's own licence material never enters a customer account.
	License collectorcredential.Secret

	Store   CredentialStore
	Clients Clients
}

// SharedInput is what the connector-wide objects depend on.
type SharedInput struct {
	// Regions is every region with a Collector. Used only to derive the ARN partition, which one
	// account is entirely inside.
	Regions []string
	Mode    string
}

// Shared is what every Collector in this connector uses.
type Shared struct {
	Partition string
	Problems  []collectorstatus.Problem
}

// Ready reports whether the shared objects are usable.
func (s Shared) Ready() bool {
	return s.Partition != "" && !collectorstatus.HasError(s.Problems)
}

// EnsureShared resolves the account partition shared by every regional Collector. IAM roles are
// customer-owned prerequisites and are never read or converged here.
func (p *Provisioner) EnsureShared(_ context.Context, in SharedInput) (Shared, error) {
	if problems := modeGuard(in.Mode); len(problems) > 0 {
		return Shared{Problems: problems}, nil
	}
	if len(in.Regions) == 0 {
		// No Collector anywhere. Nothing to provision, and nothing to report: the reason there are no
		// networks belongs to placement, which already said it.
		return Shared{}, nil
	}

	return Shared{Partition: Partition(in.Regions[0])}, nil
}

// NetworkInput is what one regional Collector's own prerequisites depend on. The name remains for
// provider compatibility; Key identifies the region and HostNetworkID is only its physical placement.
type NetworkInput struct {
	Key           basestore.CollectorKey
	HostNetworkID string
	Kind          string
	Mode          string
	// ExecutionRoleARN is the required customer-owned ECS task execution role.
	ExecutionRoleARN string
	// Shared is the result of EnsureShared and carries the account partition.
	Shared Shared
	// RetentionDays is the resolved `logRetentionDays`.
	RetentionDays int
	// Config is the configuration this Collector would be pushed over OpAMP, for the cold-start
	// bootstrap secret.
	//
	// Passed in rather than rendered here, and that is the whole point: the bootstrap document has to be
	// the SAME bytes the OpAMP server serves, or a cold start comes up on a configuration that was never
	// published and the drift is invisible. An empty Document means there is nothing to install — no
	// destinations resolved yet, or rendering was refused — and a cold start then serves the inert
	// bundled configuration, which is v1's documented behaviour rather than a fault.
	Config collectorconfig.Document
}

// Prerequisites is everything one Collector needs before its ECS service can be created.
type Prerequisites struct {
	ExecutionRoleARN string
	LogGroup         string
	// Delivery carries the two secret REFS, and has no field a value could live in.
	Delivery   collectorimage.Delivery
	Credential Credential
	Problems   []collectorstatus.Problem
}

// Ready reports whether a Collector can now be deployed. What #12 gates on.
func (p Prerequisites) Ready() bool {
	return p.ExecutionRoleARN != "" && p.LogGroup != "" && p.Credential.CollectorID != "" &&
		len(collectorimage.DeliveryProblems(p.Delivery)) == 0 &&
		!collectorstatus.HasError(p.Problems)
}

// EnsureNetwork converges one Collector's log group, its two secrets, and its OpAMP credential.
//
// Order is not arbitrary. The licence secret and the credential secret are created before anything is
// written into either, because a credential row records where its value WILL live and a secret_ref
// pointing at nothing describes a value that was minted and had nowhere to go. The credential is
// installed last, because it is the only step that is not repeatable: everything before it can be run
// again with no effect, and minting again would rotate a Collector that had no reason to rotate.
func (p *Provisioner) EnsureNetwork(ctx context.Context, in NetworkInput) (Prerequisites, error) {
	var out Prerequisites
	if problems := modeGuard(in.Mode); len(problems) > 0 {
		out.Problems = problems
		return out, nil
	}
	if !in.Shared.Ready() {
		out.Problems = []collectorstatus.Problem{{
			Code:     ProblemObjectNotOurs,
			Severity: collectorstatus.SeverityError,
			Message: fmt.Sprintf("no Collector is provisioned on %s in %s: its AWS partition could not be resolved",
				in.HostNetworkID, in.Key.Region),
		}}
		return out, nil
	}
	role, roleProblems := ValidateExecutionRoleARN(in.ExecutionRoleARN, in.Shared.Partition, p.AccountID, in.Key.Region)
	out.ExecutionRoleARN = role
	out.Problems = append(out.Problems, roleProblems...)
	if role == "" {
		return out, nil
	}

	logsAPI, err := p.Clients.Logs(ctx, in.Key.Region)
	if err != nil {
		return out, fmt.Errorf("build a CloudWatch Logs client for %s: %w", in.Key.Region, err)
	}
	logGroup, logProblems, err := EnsureLogGroup(ctx, logsAPI, LogGroupInput{
		Connector:     p.Identity.ConnectorName,
		ConnectorID:   p.Identity.ConnectorID,
		Partition:     in.Shared.Partition,
		AccountID:     p.AccountID,
		Region:        in.Key.Region,
		RetentionDays: in.RetentionDays,
		Mode:          in.Mode,
	})
	out.LogGroup = logGroup
	out.Problems = append(out.Problems, logProblems...)
	if err != nil {
		return out, err
	}

	secretsAPI, err := p.Clients.Secrets(ctx, in.Key.Region)
	if err != nil {
		return out, fmt.Errorf("build a Secrets Manager client for %s: %w", in.Key.Region, err)
	}

	licenseRef, licenseProblems, err := p.ensureLicense(ctx, secretsAPI, in)
	out.Problems = append(out.Problems, licenseProblems...)
	if err != nil {
		return out, err
	}
	out.Delivery.LicenseSecretRef = licenseRef

	credentialSecret, secretProblems, err := EnsureSecret(ctx, secretsAPI, SecretInput{
		Name:        CredentialSecretName(p.Identity.ConnectorName, in.Key.Region),
		Description: "Odigos managed Collector - OpAMP credential for " + in.Key.Region + " (host " + in.HostNetworkID + ")",
		Connector:   p.Identity.ConnectorName,
		ConnectorID: p.Identity.ConnectorID,
		Region:      in.Key.Region,
		NetworkID:   in.HostNetworkID,
		Identity:    p.Identity,
		Mode:        in.Mode,
	})
	out.Problems = append(out.Problems, secretProblems...)
	if err != nil {
		return out, err
	}
	if credentialSecret.ARN == "" {
		// Refused, and the reason is already in Problems. Returning here rather than falling through:
		// EnsureCredential with no secret ref would add "the binding is incomplete" on top of a specific,
		// actionable cause, and Ready() computes the delivery problems itself, so the gate still holds
		// without reporting them twice.
		return out, nil
	}
	out.Delivery.CredentialSecretRef = credentialSecret.ARN

	credential, credProblems, err := EnsureCredential(ctx, p.Store, secretsAPI, CredentialInput{
		Key:            in.Key,
		Kind:           in.Kind,
		Mode:           in.Mode,
		Binding:        BindingFor(p.Identity.ConnectorID, p.Provider, p.AccountID, in.Key, in.HostNetworkID),
		SecretARN:      credentialSecret.ARN,
		SecretHasValue: credentialSecret.HasValue,
	})
	out.Credential = credential
	out.Problems = append(out.Problems, credProblems...)
	if err != nil {
		return out, err
	}

	// Last, and deliberately after the credential: the bootstrap document is the only OPTIONAL half of a
	// Collector's delivery, so a failure here must not cost the Collector the two secrets it cannot run
	// without. Its own problems are reported and the rest of the delivery stands.
	bootstrapRef, bootstrapProblems, err := p.ensureBootstrap(ctx, secretsAPI, in)
	out.Problems = append(out.Problems, bootstrapProblems...)
	if err != nil {
		return out, err
	}
	out.Delivery.BootstrapSecretRef = bootstrapRef

	// The shared check both paths run: the deployer against the delivery it is about to build a task
	// definition from, the artifact guard against every published variant. Reported last so a specific
	// cause above it reads first.
	out.Problems = append(out.Problems, collectorimage.DeliveryProblems(out.Delivery)...)
	return out, nil
}

// ensureBootstrap converges the cold-start configuration secret and returns its ARN, or empty.
//
// Empty is a normal answer and not a failure. Without a bootstrap document a cold start during an outage
// of the connector or the gateway serves the inert bundled configuration — v1's documented behaviour
// — so nothing here refuses a deploy, and the ref is WITHHELD rather than returned pointing at an
// empty secret: a task definition that referenced one would fail to start with a
// ResourceInitializationError, which is the mitigation breaking the thing it exists to protect.
//
// The value is written only when the configuration has actually changed, which TagBootstrapFingerprint
// is what answers — the connector holds no permission to read the secret back. Writing
// unconditionally would mint a secret version on every reconcile pass; never rewriting would leave a
// cold start serving a configuration from before the customer's last destination change.
//
// Rewriting does not reach a RUNNING task. ECS resolves a `secrets` entry when a task starts, so a task
// holds whatever the document was then — which is exactly the semantics a startup fallback wants: the
// document a cold start serves is the one that was current when that task began.
func (p *Provisioner) ensureBootstrap(ctx context.Context, api SecretsClient, in NetworkInput) (
	string, []collectorstatus.Problem, error) {
	encoded := collectorimage.EncodeBootstrapConfig(in.Config.YAML)
	if encoded.Empty() {
		// Nothing rendered for this network yet. No secret is created either: an empty secret is an
		// object in a customer's account that describes a mitigation nobody can use, and the next pass
		// makes it the moment there is something to put in it.
		return "", nil, nil
	}

	state, problems, err := EnsureSecret(ctx, api, SecretInput{
		Name:           BootstrapSecretName(p.Identity.ConnectorName, in.Key.Region),
		Description:    "Odigos managed Collector - cold-start configuration for " + in.Key.Region + " (host " + in.HostNetworkID + ")",
		Connector:      p.Identity.ConnectorName,
		ConnectorID:    p.Identity.ConnectorID,
		Region:         in.Key.Region,
		NetworkID:      in.HostNetworkID,
		Identity:       p.Identity,
		Mode:           in.Mode,
		FingerprintTag: TagBootstrapFingerprint,
	})
	if err != nil || state.ARN == "" {
		return "", problems, err
	}

	// The CONFIGURATION's hash, not the encoded value's: it is the same identity the OpAMP push compares
	// and the same one managed_collectors.config_hash records, so a reader can tell whether the document
	// a cold start would serve is the one Odigos last published.
	if state.HasValue && state.Fingerprint == in.Config.Hash {
		return state.ARN, problems, nil
	}
	if _, err := PutSecretValue(ctx, api, state.ARN, encoded); err != nil {
		return "", problems, err
	}
	if err := RecordSecretFingerprint(ctx, api, state.ARN, TagBootstrapFingerprint, in.Config.Hash); err != nil {
		// The value landed and the marker did not, so the next pass rewrites it: one wasted secret
		// version per pass until the tag write succeeds. Not fatal, and not silent.
		log.Warn("wrote the collector cold-start configuration but could not record its fingerprint",
			"region", in.Key.Region, "network", in.HostNetworkID, "secret", state.ARN, "error", err)
	}
	log.Info("installed the collector cold-start configuration",
		"region", in.Key.Region, "network", in.HostNetworkID, "secret", state.ARN, "config", in.Config.Hash)
	return state.ARN, problems, nil
}

// ensureLicense converges the region's licence secret and returns its ARN.
//
// The value is written when the secret holds none, and rewritten only when the connector's token has
// actually changed — which is what TagLicenseFingerprint exists to answer, since the connector holds no
// permission to read the secret back. Writing unconditionally would mint a new secret version on every
// reconcile pass; never rewriting would mean a renewed licence never reaches the Collector, and
// makes that a crash loop rather than a degradation.
//
// Rewriting the secret does NOT get the new licence into a running task: like a credential rotation, a
// running task holds what was injected at start, so the redeploy is what installs it. That redeploy is
// #12's, driven by the task definition changing when the image or the secret version does.
func (p *Provisioner) ensureLicense(ctx context.Context, api SecretsClient, in NetworkInput) (string, []collectorstatus.Problem, error) {
	state, problems, err := EnsureSecret(ctx, api, SecretInput{
		Name:        LicenseSecretName(p.Identity.ConnectorName, in.Key.Region),
		Description: "Odigos managed Collector - enterprise licence token for " + in.Key.Region,
		Connector:   p.Identity.ConnectorName,
		ConnectorID: p.Identity.ConnectorID,
		Region:      in.Key.Region,
		// No network: one licence secret serves every Collector in the region.
		Identity:       p.Identity,
		Mode:           in.Mode,
		FingerprintTag: TagLicenseFingerprint,
	})
	if err != nil || state.ARN == "" {
		return "", problems, err
	}

	if p.License.Empty() {
		// Reported rather than written empty, and the ref is withheld so nothing downstream builds a
		// task definition around a secret with nothing in it. The Collector's enterprise auth extension
		// refuses to start without a valid token, so deploying anyway produces a crash loop.
		return "", append(problems, collectorstatus.Problem{
			Code:     ProblemLicenseTokenMissing,
			Severity: collectorstatus.SeverityError,
			Message: "the connector holds no " + collectorimage.EnvOnPremToken + ", so there is nothing to copy " +
				"into this account's licence secret. The managed Collector refuses to start without one. The " +
				"connector reads the same value every other enterprise component in this install uses, from the " +
				"odigos-pro secret — check that it is present and that the connector Deployment is stamped with it",
		}), nil
	}

	fingerprint := p.License.Fingerprint()
	if state.HasValue && state.Fingerprint == fingerprint {
		return state.ARN, problems, nil
	}
	if _, err := PutSecretValue(ctx, api, state.ARN, p.License); err != nil {
		return "", problems, err
	}
	if err := RecordSecretFingerprint(ctx, api, state.ARN, TagLicenseFingerprint, fingerprint); err != nil {
		// The value landed and the marker did not, so the next pass rewrites the value: one wasted
		// secret version per pass until the tag write succeeds. Not fatal, and not silent.
		log.Warn("wrote the collector licence secret but could not record its fingerprint",
			"region", in.Key.Region, "secret", state.ARN, "error", err)
	}
	log.Info("installed collector licence secret",
		"region", in.Key.Region, "secret", state.ARN, "fingerprint", fingerprint)
	return state.ARN, problems, nil
}

// AWSPlatform is what AWS's ingress can terminate, for collectoringress.ResolveTransport.
//
// Exported because the OBSERVER resolves the same endpoint from the same load balancer: two values here
// would let the endpoint Odigos publishes and the endpoint Odigos reports back differ, which reads as
// drift on a Collector that has none.
//
// ManagedTLS is false and that is not an omission: a Network Load Balancer's generated DNS name is in
// a domain Odigos does not control, so no certificate can be issued for it and there is nothing for
// AWS to terminate TLS with by default. LoadBalancerTLS is true because a customer who owns a hostname
// can put their own ACM certificate on the listeners.
var AWSPlatform = collectoringress.Platform{Name: "AWS", ManagedTLS: false, LoadBalancerTLS: true}

// Endpoint sources, as managed_collectors.endpoint_source records them.
const (
	// EndpointSourceLoadBalancer: the NLB Odigos created.
	EndpointSourceLoadBalancer = "lb"
	// EndpointSourceOverride: the customer's own front, via endpointOverride.
	EndpointSourceOverride = "override"
)

// IngressInput is what converging one regional Collector's data plane depends on.
type IngressInput struct {
	Key  basestore.CollectorKey
	Mode string
	// Shared is the result of EnsureShared, for the ARN partition.
	Shared Shared
	// VPCID and Subnets come from placement. Subnets are what the load balancer answers in.
	VPCID   string
	Subnets []string
	// NetworkBlocks are the VPC's own CIDR associations — the ONLY thing the fallback may name.
	// Never a peered range, a gateway route, or a block Odigos synthesised.
	NetworkBlocks []string
	// Sources are the enabled workloads routed to this Collector, with the security groups they send
	// from. A workload with no resolved group is not "no group" — see awsingress.Source.
	Sources []awsingress.Source
	// UnresolvedCycles is how many consecutive discovery cycles have resolved zero security groups. It
	// is what holds the network-CIDR fallback back while discovery is merely incomplete.
	UnresolvedCycles int
	// Config and Transport are the resolved `ingress` and `tls` + `endpointOverride` trees.
	Config    collectoringress.Config
	Transport collectoringress.TransportConfig
}

// Ingress is one network's converged data plane.
//
// Every ref here has to reach managed_collectors, because a recorded ref is HALF the deletion gate
// : an object whose tag says Odigos created it and whose ref is not recorded cannot be deleted
// at teardown. This package does not write the row itself — the load balancer refs and the ECS service
// refs live in one column group that one accessor writes whole, so a write from here would null the
// refs #12 puts there and quietly strip delete authority from the ECS service. The reconcile pass
// records both at once.
type Ingress struct {
	// SecurityGroupID is attached to the load balancer AND to the Collector's tasks, which is what
	// makes the health check on 13133 admissible from the Collector's own group.
	SecurityGroupID string
	LoadBalancerARN string
	// TargetGroupARNs are in data-port order — 4317 then 4318 — which is the order #12's ECS service
	// registers its container ports into.
	TargetGroupARNs []string
	// Transport is the endpoint workloads are pointed at, and what that hop costs.
	Transport collectoringress.Transport
	// EndpointSource is what managed_collectors.endpoint_source records.
	EndpointSource string
	// Tier is which class of ingress rule the decision landed on, for managed_collectors.ingress_tier.
	Tier collectoringress.Tier
	// Authorized and Revoked are what this pass changed. Both zero is the steady state, and a non-zero
	// Revoked is what narrowing back down from a widened fallback looks like.
	Authorized int
	Revoked    int
	Problems   []collectorstatus.Problem
}

// Ready reports whether workloads can be pointed at this Collector.
func (i Ingress) Ready() bool {
	return i.SecurityGroupID != "" && i.Transport.GRPCEndpoint != "" &&
		!collectorstatus.HasError(i.Problems)
}

// EnsureIngress converges the network rules and the load balancer in front of one Collector.
//
// The order is forced, not chosen. Authorization is decided FIRST, by collectoringress.Resolve, because
// a decision not to deploy has to stop everything: the fallback refusals exist so that a network
// whose senders Odigos cannot identify gets no Collector rather than a Collector the whole network can
// write to. Then the security group, because an NLB must be created with one attached and can never
// have one added afterwards. Then the load balancer, its target groups and its listeners.
//
// `endpointOverride` skips the load balancer entirely and keeps everything else: the customer's own
// front still sends to the Collector's tasks, so the tasks still need a security group admitting it.
func (p *Provisioner) EnsureIngress(ctx context.Context, in IngressInput) (Ingress, error) {
	var out Ingress
	if problems := modeGuard(in.Mode); len(problems) > 0 {
		out.Problems = problems
		return out, nil
	}

	// The transport configuration is validated before anything is written, with no host yet: a
	// contradictory `tls` tree (loadBalancer mode with no hostname, a TLS listener alongside an
	// endpointOverride) is a configuration error, and creating a load balancer and then refusing to
	// point anything at it would leave a chargeable object serving nothing.
	if _, problems := collectoringress.ResolveTransport(in.Transport, AWSPlatform, ""); collectorstatus.HasError(problems) {
		out.Problems = problems
		return out, nil
	}

	decision := awsingress.Resolve(awsingress.Input{
		Sources:          in.Sources,
		Config:           in.Config,
		NetworkBlocks:    in.NetworkBlocks,
		UnresolvedCycles: in.UnresolvedCycles,
	})
	out.Tier, out.Problems = decision.Tier, decision.Problems
	if !decision.Deploy {
		// Nothing is written, and an existing group is left exactly as it is. Narrowing it to nothing on
		// a refusal would take a working Collector offline, which is the opposite of what the refusal is
		// protecting against.
		return out, nil
	}

	sgAPI, err := p.Clients.EC2(ctx, in.Key.Region)
	if err != nil {
		return out, fmt.Errorf("build an EC2 client for %s: %w", in.Key.Region, err)
	}
	group, sgProblems, err := EnsureSecurityGroup(ctx, sgAPI, SecurityGroupInput{
		Region:      in.Key.Region,
		NetworkID:   in.VPCID,
		VPCID:       in.VPCID,
		Connector:   p.Identity.ConnectorName,
		ConnectorID: p.Identity.ConnectorID,
		Identity:    p.Identity,
		Mode:        in.Mode,
		Rules:       decision.Rules,
	})
	out.Problems = append(out.Problems, sgProblems...)
	if err != nil {
		return out, err
	}
	if group.ID == "" {
		return out, nil
	}
	out.SecurityGroupID, out.Authorized, out.Revoked = group.ID, group.Authorized, group.Revoked

	host := ""
	out.EndpointSource = EndpointSourceOverride
	if in.Transport.EndpointOverride == "" {
		out.EndpointSource = EndpointSourceLoadBalancer

		elbAPI, err := p.Clients.LoadBalancers(ctx, in.Key.Region)
		if err != nil {
			return out, fmt.Errorf("build an Elastic Load Balancing client for %s: %w", in.Key.Region, err)
		}
		lb, lbProblems, err := EnsureLoadBalancer(ctx, elbAPI, LoadBalancerInput{
			Region:      in.Key.Region,
			NetworkID:   in.VPCID,
			VPCID:       in.VPCID,
			Connector:   p.Identity.ConnectorName,
			ConnectorID: p.Identity.ConnectorID,
			Identity:    p.Identity,
			Mode:        in.Mode,
			Subnets:     in.Subnets,
			// The group has to exist before this call, and this is the reason the two steps are ordered
			// rather than concurrent.
			SecurityGroupID:   group.ID,
			TLSCertificateARN: listenerCertificate(in.Transport),
		})
		out.Problems = append(out.Problems, lbProblems...)
		if err != nil {
			return out, err
		}
		out.LoadBalancerARN, out.TargetGroupARNs = lb.ARN, lb.TargetGroupARNs
		host = lb.DNSName
	}

	// Resolved again, now with the address that exists. An empty host is not an error here — it is the
	// first pass, before the load balancer has a DNS name — and ResolveTransport reports it as Pending
	// so nothing is pointed at an endpoint that does not resolve yet.
	transport, transportProblems := collectoringress.ResolveTransport(in.Transport, AWSPlatform, host)
	out.Transport = transport
	out.Problems = append(out.Problems, transportProblems...)
	return out, nil
}

// listenerCertificate is the ACM certificate the data listeners carry, and only for the one mode that
// puts a certificate on them.
//
// `tls.mode: collector` deliberately returns nothing: there the Collector terminates TLS itself from a
// certificate in the secret store, so the listeners stay TCP and pass the encrypted stream through. A
// TLS listener there would terminate the customer's own session at the load balancer and re-open a
// plaintext one behind it — the opposite of what the mode asks for.
func listenerCertificate(cfg collectoringress.TransportConfig) string {
	if cfg.Mode == collectoringress.TLSLoadBalancer {
		return cfg.CertificateRef
	}
	return ""
}

// WorkloadInput is what converging one regional Collector's runnable definition depends on. The host VPC
// is placement metadata and is kept separate from the region-only Collector key.
type WorkloadInput struct {
	Key           basestore.CollectorKey
	HostNetworkID string
	Mode          string
	// Prerequisites is the result of EnsureNetwork: the roles, the log group and the two secret refs.
	// Passed in rather than re-derived so the identity half runs once per network per pass.
	Prerequisites Prerequisites
	// Settings is the resolved tree for this network — size, architecture, image override, the
	// cluster the customer pointed at, and the Fargate Spot opt-in.
	Settings collectorsettings.Network
	// OpAMPEndpoint is the public endpoint the Collector dials back to.
	OpAMPEndpoint string
	// Pin is the image this connector build deploys. Supplied rather than read from the package so a
	// test can pin a digest without the compiled-in one being resolvable.
	Pin collectorimage.Pin
}

// Workload is the cluster and the revision a Collector service will run.
type Workload struct {
	Cluster        Cluster
	TaskDefinition TaskDefinition
	Problems       []collectorstatus.Problem
}

// Ready reports whether a service could now be created. What the service converge gates on.
func (w Workload) Ready() bool {
	return w.Cluster.ARN != "" && w.TaskDefinition.Ready() && !collectorstatus.HasError(w.Problems)
}

// EnsureWorkload converges the regional cluster and this network's task definition.
//
// The cluster first, because a revision is useless without somewhere to run it and creating the cluster
// is the cheaper of the two to repeat. Both are idempotent, and the steady state performs zero AWS
// writes: the cluster is described and left alone, and the task definition is registered only when the
// rendered definition differs from the family's ACTIVE one.
//
// Nothing here creates or updates a service. That is deliberate — the revision and the cluster are what
// a service is built FROM, and separating them keeps "the definition changed" from meaning "the fleet is
// rolling" until something decides to roll it.
func (p *Provisioner) EnsureWorkload(ctx context.Context, in WorkloadInput) (Workload, error) {
	var out Workload
	if problems := modeGuard(in.Mode); len(problems) > 0 {
		out.Problems = problems
		return out, nil
	}
	if !in.Prerequisites.Ready() {
		// The roles, the log group and the two secrets are all named BY the task definition, so a
		// definition registered without them would be a revision that cannot start — and a revision is
		// permanent, unlike the failure that produced it.
		out.Problems = []collectorstatus.Problem{{
			Code:     ProblemObjectNotOurs,
			Severity: collectorstatus.SeverityError,
			Message: fmt.Sprintf("no Collector task definition is registered for %s in %s: its identity, log "+
				"group or secrets are not ready, and a revision naming any of them before they exist could "+
				"never start a task", in.HostNetworkID, in.Key.Region),
		}}
		return out, nil
	}

	api, err := p.Clients.ECS(ctx, in.Key.Region)
	if err != nil {
		return out, fmt.Errorf("build an ECS client for %s: %w", in.Key.Region, err)
	}

	cluster, clusterProblems, err := EnsureCluster(ctx, api, ClusterInput{
		Region:     in.Key.Region,
		Mode:       in.Mode,
		Ref:        in.Settings.ClusterRef,
		SpotWanted: in.Settings.UseFargateSpot,
	})
	out.Cluster = cluster
	out.Problems = append(out.Problems, clusterProblems...)
	if err != nil {
		return out, err
	}
	if cluster.ARN == "" {
		return out, nil
	}

	definition, err := EnsureTaskDefinition(ctx, api, TaskDefinitionInput{
		HostNetworkID:           in.HostNetworkID,
		Region:                  in.Key.Region,
		Connector:               p.Identity.ConnectorName,
		ConnectorID:             p.Identity.ConnectorID,
		Identity:                p.Identity,
		Mode:                    in.Mode,
		ExecutionRoleARN:        in.Prerequisites.ExecutionRoleARN,
		LogGroup:                in.Prerequisites.LogGroup,
		Delivery:                in.Prerequisites.Delivery,
		OpAMPEndpoint:           in.OpAMPEndpoint,
		Pin:                     in.Pin,
		Arch:                    in.Settings.Arch,
		CPU:                     in.Settings.CPU,
		Memory:                  in.Settings.Memory,
		ImageRepositoryOverride: in.Settings.ImageRepositoryOverride,
	})
	out.TaskDefinition = definition
	out.Problems = append(out.Problems, definition.Problems...)
	return out, err
}

// ServiceStateInput is what the service converge needs beyond what the earlier halves produced.
type ServiceStateInput struct {
	Key           basestore.CollectorKey
	HostNetworkID string
	Mode          string
	// Workload is the result of EnsureWorkload: the cluster and the revision to run.
	Workload Workload
	// Ingress is the result of EnsureIngress: the security group and the target groups.
	Ingress Ingress
	// Subnets are placement's chosen subnets — the same ones the load balancer answers in.
	Subnets []string
	// Settings is the resolved tree: the scaling policy, the Spot opt-in, `assignPublicIp`, and any
	// security groups the customer supplied instead of relying on Odigos's.
	Settings collectorsettings.Network
	// Recorded is whether managed_collectors already names this service — half the deletion gate
	// and what tells this connector's own service apart from one missing from its state.
	Recorded bool
}

// EnsureCollectorService converges one regional ECS service, which is the step that makes a Collector
// actually run.
//
// Last, and dependent on everything: it names the cluster, the revision, the subnets, the security group
// and the target groups, so each of those has to exist and be Odigos's to use before a service can point
// at it. Refusing early is what keeps a permanent object — a service describes a Collector for as long as
// it exists — from being created around a transient failure.
func (p *Provisioner) EnsureCollectorService(ctx context.Context, in ServiceStateInput) (Service, error) {
	if problems := modeGuard(in.Mode); len(problems) > 0 {
		return Service{Name: ServiceName(in.Key.Region), Problems: problems}, nil
	}
	if !in.Workload.Ready() || !in.Ingress.Ready() {
		return Service{
			Name: ServiceName(in.Key.Region),
			Problems: []collectorstatus.Problem{{
				Code:     ProblemServiceNotReady,
				Severity: collectorstatus.SeverityError,
				Message: fmt.Sprintf("no Collector service is created for %s in %s: its cluster, task "+
					"definition or network ingress is not ready. The specific reason is reported against "+
					"whichever of those it is", in.Key.Region, in.HostNetworkID),
			}},
		}, nil
	}

	api, err := p.Clients.ECS(ctx, in.Key.Region)
	if err != nil {
		return Service{}, fmt.Errorf("build an ECS client for %s: %w", in.Key.Region, err)
	}

	return EnsureService(ctx, api, ServiceInput{
		HostNetworkID:       in.HostNetworkID,
		Region:              in.Key.Region,
		Connector:           p.Identity.ConnectorName,
		ConnectorID:         p.Identity.ConnectorID,
		Identity:            p.Identity,
		Mode:                in.Mode,
		ClusterARN:          in.Workload.Cluster.ARN,
		ClusterSupportsSpot: in.Workload.Cluster.SupportsSpot(),
		TaskDefinitionARN:   in.Workload.TaskDefinition.ARN,
		Subnets:             in.Subnets,
		SecurityGroupIDs:    serviceSecurityGroups(in),
		AssignPublicIP:      in.Settings.AssignPublicIP,
		TargetGroupARNs:     in.Ingress.TargetGroupARNs,
		Scaling:             in.Settings.Scaling.Resolve(),
		UseFargateSpot:      in.Settings.UseFargateSpot,
		Recorded:            in.Recorded,
	})
}

// serviceSecurityGroups are the groups the Collector's tasks run in.
//
// Odigos's own group unless the customer supplied their own, in which case theirs REPLACE it rather than
// joining it — `collector.regions.<region>.securityGroupIds` is documented as "supply your own instead of
// the created one", and a union would mean the rules Odigos converges still applied to a customer
// who deliberately took that over. Odigos's group stays attached to the load balancer either way, which
// is what keeps the health check working.
func serviceSecurityGroups(in ServiceStateInput) []string {
	if len(in.Settings.SecurityGroupIDs) > 0 {
		return in.Settings.SecurityGroupIDs
	}
	return []string{in.Ingress.SecurityGroupID}
}

// ScalingStateInput is what converging one regional Collector's replica policy needs beyond what the earlier
// halves produced.
type ScalingStateInput struct {
	Key           basestore.CollectorKey
	HostNetworkID string
	Mode          string
	// Workload is the result of EnsureWorkload. Only the cluster is read, and specifically its NAME:
	// Application Auto Scaling addresses an ECS service by `service/<cluster name>/<service name>` and
	// refuses the ARN every other call in this package passes around.
	Workload Workload
	// Service is the result of EnsureCollectorService. Its ARN is how this knows there is a service to
	// govern at all.
	Service Service
	// Settings is the resolved tree; only the scaling policy is read.
	Settings collectorsettings.Network
	// Recorded is whether managed_collectors names this Collector — half the deletion gate, and
	// a deregistration is a delete.
	Recorded bool
}

// EnsureCollectorScaling converges one regional Collector's replica policy: a fixed count with no autoscaling
// resource, or a scalable target and the two target-tracking policies specifies.
//
// Runs AFTER the service converge, and that ordering is a constraint rather than a preference:
// RegisterScalableTarget on a service Application Auto Scaling cannot find is refused, so a pass that
// registered first would fail on every fresh network. Nothing forces the ordering the other way — a
// describe of a target that was never registered comes back empty rather than failing — which is why
// this can safely run on the very first pass, before anything exists.
func (p *Provisioner) EnsureCollectorScaling(ctx context.Context, in ScalingStateInput) (ScalingState, error) {
	// Resolved here, once, and handed to EnsureScaling. The service converge resolves the same policy
	// from the same settings, which is safe because Resolve is pure — what would not be safe is two
	// places each deciding for themselves what an unrecognised mode means.
	resolved := in.Settings.Scaling.Resolve()
	if problems := modeGuard(in.Mode); len(problems) > 0 {
		return ScalingState{Mode: resolved.Mode, Problems: problems}, nil
	}

	api, err := p.Clients.AutoScaling(ctx, in.Key.Region)
	if err != nil {
		return ScalingState{}, fmt.Errorf("build an Application Auto Scaling client for %s: %w", in.Key.Region, err)
	}

	return EnsureScaling(ctx, api, ScalingInput{
		HostNetworkID: in.HostNetworkID,
		Region:        in.Key.Region,
		Connector:     p.Identity.ConnectorName,
		ConnectorID:   p.Identity.ConnectorID,
		Identity:      p.Identity,
		Mode:          in.Mode,
		ClusterName:   in.Workload.Cluster.Name,
		ServiceARN:    in.Service.ARN,
		Scaling:       resolved,
		Recorded:      in.Recorded,
	})
}

// InfrastructureRecord assembles the one CollectorRecord the reconcile pass writes through
// basestore.RecordCollectorInfrastructure.
//
// One assembler because there is one writer: that accessor writes all seven refs together, so two
// callers each filling in their own half would null the other's — and a ref that is not recorded is an
// object teardown may never delete. Every converge half returns refs; this is where they meet.
func InfrastructureRecord(key basestore.CollectorKey, workload Workload, ingress Ingress, service Service, subnets []string) basestore.CollectorRecord {
	return basestore.CollectorRecord{
		Key:           key,
		WorkloadRef:   service.ARN,
		RevisionRef:   workload.TaskDefinition.ARN,
		RuntimeRef:    workload.Cluster.ARN,
		FrontendRef:   ingress.LoadBalancerARN,
		BackendRefs:   ingress.TargetGroupARNs,
		IngressRef:    ingress.SecurityGroupID,
		PlacementRefs: subnets,
	}
}

// ScalingObservation is the one set of scaling facts a reconcile pass records through
// basestore.RecordCollectorScaling.
//
// One assembler for the same reason InfrastructureRecord is one: that accessor writes the mode, both
// counts, both bounds and the task size TOGETHER, so a caller filling in only the half it knows would
// null the other half. Here the halves come from three different places — the resolved configuration,
// the service converge, and the workload's size — which is exactly the shape that goes wrong.
type ScalingObservation struct {
	Mode         string
	DesiredCount *int
	RunningCount *int
	MinCount     *int
	MaxCount     *int
	CPU          string
	Memory       string
}

// ObservedScaling assembles what is governing and running this Collector.
//
// What is IN FORCE rather than what was configured, which is the distinction that makes the row worth
// reading:
//
//   - a mode this connector does not implement records NO mode, rather than writing a typo into a column
//     documented as manual | auto. Nothing is driving the count, and a null says that.
//   - the bounds are recorded only while a scalable target exists, so a row carrying bounds is a row
//     where something is autoscaling — whoever registered it.
//   - the counts are recorded only once there is a service. Nil is "no service yet"; a recorded zero is
//     a service running no tasks, which is a different and much more interesting state.
func ObservedScaling(settings basecollectorsettings.Network, service Service, scaling ScalingState) ScalingObservation {
	resolved := settings.Scaling.Resolve()
	out := ScalingObservation{CPU: settings.CPU, Memory: settings.Memory}
	if resolved.Known() {
		out.Mode = resolved.Mode
	}
	if service.ARN != "" {
		out.DesiredCount = countRef(int(service.DesiredCount))
		out.RunningCount = countRef(int(service.RunningCount))
	}
	if scaling.TargetRegistered {
		out.MinCount = countRef(scaling.MinCount)
		out.MaxCount = countRef(scaling.MaxCount)
	}
	return out
}

// countRef takes the address of a copy, so a caller cannot alias the observation's fields together.
func countRef(n int) *int { return &n }
