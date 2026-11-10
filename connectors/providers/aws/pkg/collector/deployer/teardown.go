package deployer

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	aas "github.com/aws/aws-sdk-go-v2/service/applicationautoscaling"
	aastypes "github.com/aws/aws-sdk-go-v2/service/applicationautoscaling/types"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"
	logstypes "github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs/types"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"
	elb "github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2"
	elbtypes "github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2/types"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"
	"github.com/aws/smithy-go"

	baseapi "github.com/odigos-io/odigos/connectors/base/api"
	"github.com/odigos-io/odigos/connectors/base/domain/collectorcredential"
	"github.com/odigos-io/odigos/connectors/base/domain/collectoringress"
	"github.com/odigos-io/odigos/connectors/base/domain/collectorownership"
	"github.com/odigos-io/odigos/connectors/base/domain/collectorstatus"
	basestore "github.com/odigos-io/odigos/connectors/base/store"
	"github.com/odigos-io/odigos/connectors/spec"
)

// Teardown removes what Odigos created for one Collector.
//
// # Why the destructive verbs live in their own interfaces
//
// None of the client interfaces the converge path takes contains a delete. `ServiceClient` has no
// DeleteService, `SecretsClient` has no DeleteSecret, `LoadBalancerClient` has no DeleteLoadBalancer —
// so `EnsureService` cannot delete a service however it is called, and a describe that came back empty
// cannot be turned into a deletion by a bug three functions away. The interfaces below are the only
// place those verbs exist, and teardown_test.go fails if one appears in any other file in the package.
// One AWS client satisfies both halves; the seam is in the signatures, which is where it holds.
//
// # The one destructive verb the converge path DOES hold
//
// `DeregisterScalableTarget`, because switching a network from `auto` to `manual` scaling has to remove
// the target Odigos registered — and that call is already gated on the same two records this
// file gates everything on. It is the exception that is written down rather than the exception nobody
// noticed.

// TeardownClients supplies the destructive AWS clients, per region where that matters.
//
// A separate interface from Clients rather than more methods on it: a Provisioner holding one interface
// with both halves would put a delete within reach of every Ensure* function on it.
type TeardownClients interface {
	Logs(ctx context.Context, region string) (LogGroupTeardownClient, error)
	Secrets(ctx context.Context, region string) (SecretTeardownClient, error)
	EC2(ctx context.Context, region string) (SecurityGroupTeardownClient, error)
	LoadBalancers(ctx context.Context, region string) (LoadBalancerTeardownClient, error)
	ECS(ctx context.Context, region string) (ECSTeardownClient, error)
	AutoScaling(ctx context.Context, region string) (ScalingTeardownClient, error)
}

// Each interface below contains only the reads and mutations teardown needs. The describe is not
// optional: an object's TAGS are half the deletion gate and none of these refs carries them.
type (
	// ECSTeardownClient covers only the service installed in the customer-owned cluster.
	//
	// DeregisterTaskDefinition is deliberately absent. list does not include the revision, and
	// the reason is worth stating: a task-definition revision costs nothing, holds no secret value (its
	// `secrets` block holds refs), and cannot run once the service naming it is gone. Deregistering is
	// also one-way — an INACTIVE revision cannot be reactivated — so the only thing it would buy is a
	// tidier console listing, at the price of a call that can fail during a teardown that has already
	// deleted the service.
	ECSTeardownClient interface {
		DescribeServices(ctx context.Context, in *ecs.DescribeServicesInput, opts ...func(*ecs.Options)) (*ecs.DescribeServicesOutput, error)
		DeleteService(ctx context.Context, in *ecs.DeleteServiceInput, opts ...func(*ecs.Options)) (*ecs.DeleteServiceOutput, error)
		ListTasks(ctx context.Context, in *ecs.ListTasksInput, opts ...func(*ecs.Options)) (*ecs.ListTasksOutput, error)
		StopTask(ctx context.Context, in *ecs.StopTaskInput, opts ...func(*ecs.Options)) (*ecs.StopTaskOutput, error)
	}

	// LoadBalancerTeardownClient covers the NLB and its target groups. Its listeners are not listed
	// here: deleting a load balancer deletes them, and a listener is not an object a customer names.
	LoadBalancerTeardownClient interface {
		DescribeLoadBalancers(ctx context.Context, in *elb.DescribeLoadBalancersInput, opts ...func(*elb.Options)) (*elb.DescribeLoadBalancersOutput, error)
		DescribeTargetGroups(ctx context.Context, in *elb.DescribeTargetGroupsInput, opts ...func(*elb.Options)) (*elb.DescribeTargetGroupsOutput, error)
		DescribeTags(ctx context.Context, in *elb.DescribeTagsInput, opts ...func(*elb.Options)) (*elb.DescribeTagsOutput, error)
		DeleteLoadBalancer(ctx context.Context, in *elb.DeleteLoadBalancerInput, opts ...func(*elb.Options)) (*elb.DeleteLoadBalancerOutput, error)
		DeleteTargetGroup(ctx context.Context, in *elb.DeleteTargetGroupInput, opts ...func(*elb.Options)) (*elb.DeleteTargetGroupOutput, error)
	}

	SecurityGroupTeardownClient interface {
		DescribeSecurityGroups(ctx context.Context, in *ec2.DescribeSecurityGroupsInput, opts ...func(*ec2.Options)) (*ec2.DescribeSecurityGroupsOutput, error)
		DeleteSecurityGroup(ctx context.Context, in *ec2.DeleteSecurityGroupInput, opts ...func(*ec2.Options)) (*ec2.DeleteSecurityGroupOutput, error)
	}

	SecretTeardownClient interface {
		DescribeSecret(ctx context.Context, in *secretsmanager.DescribeSecretInput, opts ...func(*secretsmanager.Options)) (*secretsmanager.DescribeSecretOutput, error)
		DeleteSecret(ctx context.Context, in *secretsmanager.DeleteSecretInput, opts ...func(*secretsmanager.Options)) (*secretsmanager.DeleteSecretOutput, error)
	}

	// LogGroupTeardownClient reads tags through their own call, because DescribeLogGroups does not
	// return them — the same shape as ELBv2.
	LogGroupTeardownClient interface {
		ListTagsForResource(ctx context.Context, in *cloudwatchlogs.ListTagsForResourceInput, opts ...func(*cloudwatchlogs.Options)) (*cloudwatchlogs.ListTagsForResourceOutput, error)
		DeleteLogGroup(ctx context.Context, in *cloudwatchlogs.DeleteLogGroupInput, opts ...func(*cloudwatchlogs.Options)) (*cloudwatchlogs.DeleteLogGroupOutput, error)
	}

	// ScalingTeardownClient deregisters the scalable target, which takes its policies with it.
	ScalingTeardownClient interface {
		DescribeScalableTargets(ctx context.Context, in *aas.DescribeScalableTargetsInput, opts ...func(*aas.Options)) (*aas.DescribeScalableTargetsOutput, error)
		ListTagsForResource(ctx context.Context, in *aas.ListTagsForResourceInput, opts ...func(*aas.Options)) (*aas.ListTagsForResourceOutput, error)
		DeregisterScalableTarget(ctx context.Context, in *aas.DeregisterScalableTargetInput, opts ...func(*aas.Options)) (*aas.DeregisterScalableTargetOutput, error)
	}
)

// TeardownStore is the slice of the store teardown uses.
//
// ListManagedCollectors is here for one reason: the shared objects — the region's cluster and log
// group and the region's licence secret may only be removed when no other
// network still needs them, and this is the only thing that can answer that.
type TeardownStore interface {
	GetManagedCollector(ctx context.Context, key basestore.CollectorKey) (basestore.CollectorRecord, error)
	ListManagedCollectors(ctx context.Context) ([]basestore.CollectorRecord, error)
	RevokeCollectorCredentials(ctx context.Context, collectorID string, reason collectorcredential.Reason, actor collectorcredential.Actor) ([]collectorcredential.Record, error)
	DeleteManagedCollector(ctx context.Context, key basestore.CollectorKey) error
}

// TeardownInput is what removing one network's Collector depends on.
type TeardownInput struct {
	Key       basestore.CollectorKey
	NetworkID string // physical host VPC; the Collector identity remains regional
	Mode      string
	// Reason separates the two operations gives the same first step and different second ones.
	// ReasonTeardown revokes and then deletes; ReasonCapabilityRemoved revokes and deletes NOTHING,
	// because an older connector image that cannot parse the `collectors` key is indistinguishable from
	// a capability a customer removed — so deleting on absence would turn an image
	// rollback into a customer's Collector disappearing.
	Reason collectorcredential.Reason
	// Identity is this connector, as collectorownership reads it. Taken from the running process like
	// everywhere else in this package: a recreated connector reads its predecessor's stack as
	// AttributionPredecessor, which is Mine — so the stack it adopted is the stack it can remove.
	Identity collectorownership.Identity
	// Partition and AccountID compose the log group's ARN, which is the only way to read its tags.
	Partition string
	AccountID string
	// CustomerSecurityGroupIDs names objects the customer pointed Odigos at, which are never deleted
	// whatever their tags say.
	CustomerSecurityGroupIDs []string
}

// KeptObject is one object teardown did not remove, and why.
//
// Reported rather than logged, because failure mode is silently leaked infrastructure: an
// object Odigos declined to delete has to be visible to whoever now owns it. Declining is not the
// failure — being quiet about it is.
type KeptObject struct {
	Kind   string
	Ref    string
	Reason string
	// Leaked is the one retention that is nobody's design: the object carries Odigos's own ownership
	// marker and is attributed to this connector, and the connector's state does not record its ref. So
	// Odigos created it, cannot delete it, and — if the row went — would have nothing left that could
	// ever authorize deleting it.
	//
	// Every other retention is fine: an adopted stack, a customer-named cluster and another connector's
	// object are all things Odigos never created and is right to leave alone.
	Leaked bool
}

// TeardownResult is what one network's teardown did.
type TeardownResult struct {
	// Removed names every object deleted, in the order they went.
	Removed []string
	// Kept names every object left standing, with the reason.
	Kept []KeptObject
	// RevokedCredentials is how many live credentials were revoked. Two during a rotation.
	RevokedCredentials int
	// RecordRemoved reports whether the managed_collectors row was deleted, which happens only when
	// nothing deletable is left standing — the refs in it are half the authority over anything that is.
	RecordRemoved bool
	Problems      []collectorstatus.Problem
}

// Complete reports whether this network needs no further teardown pass.
func (r TeardownResult) Complete() bool { return r.RecordRemoved }

// Teardown removes one network's Collector, credentials first.
//
// # The ordering, and which part of it is a safety property
//
// Credentials are revoked BEFORE anything is deleted, and a failure to revoke stops the teardown
// entirely. This inverts routine rotation, which revokes last, and the reason is that here the
// connection is meant to die: a task that outlives the delete call — draining, stuck, or held by a
// deployment that will not settle — would otherwise keep an authenticated OpAMP session open and keep
// being handed the desired configuration, which carries the destination's own credential. Deleting the
// compute first and revoking afterwards would make that window as long as the AWS calls take; deleting
// it after a revocation that FAILED would make the window unbounded.
//
// The AWS order after that is forced by dependency rather than chosen: the scalable target before the
// service it addresses, the load balancer before the target groups its listeners forward to, both
// before the security group whose network interfaces they hold, and the row last of all.
//
// # Every object, one at a time
//
// Teardown is per object, not per stack. A customer-named cluster with an Odigos service registered in
// it loses the service and keeps the cluster; an adopted load balancer is converged for its whole life
// and then left exactly where it is. `collectorownership.MayDelete` is the only thing that decides, and
// it is read here rather than re-derived — a teardown path that re-read raw tags would be free to
// disagree with the converge path, and the disagreement is a customer's infrastructure deleted.
//
// # Resumable, and never partial in a way that loses authority
//
// Every step treats "already gone" as success and every failure leaves the row in place, so a teardown
// interrupted halfway is finished by the next pass. That is what the row-last ordering buys: the refs
// are half the deletion gate, so a row deleted while an object still stands strips the authority to
// remove that object for ever, and the object becomes infrastructure nothing records and nobody
// owns.
func Teardown(ctx context.Context, clients TeardownClients, store TeardownStore, in TeardownInput) (TeardownResult, error) {
	var out TeardownResult

	record, err := store.GetManagedCollector(ctx, in.Key)
	switch {
	case errors.Is(err, basestore.ErrCollectorNotFound):
		// Nothing recorded. Not an error and not a no-op worth hiding: without a record no object passes
		// the deletion gate, so anything Odigos built for this network is now unownable — which is the
		// direction chooses to fail in, and a state a customer has to be told about because only a
		// tag scan (or a manual deletion) can resolve it.
		out.Problems = append(out.Problems, collectorstatus.Problem{
			Code:     ProblemTeardownUnrecorded,
			Severity: collectorstatus.SeverityDegraded,
			Message: fmt.Sprintf("no Collector is recorded for %s in %s, so nothing is deleted for it: an "+
				"object may only be removed when its tags say Odigos created it AND this connector's own "+
				"state records its ref. If a Collector is running on that network, it has to be removed by "+
				"hand or re-adopted first", in.NetworkID, in.Key.Region),
		})
		return out, nil
	case err != nil:
		return out, fmt.Errorf("reading the recorded state of the Collector in %s: %w", in.Key.Region, err)
	}
	if in.NetworkID == "" {
		in.NetworkID = record.NetworkID
	}

	if in.Mode != spec.ModeAutomatic {
		// in manual mode teardown deletes nothing, whatever the tags say. This is what makes
		// "delete the connector and confirm nothing in the customer's account is touched" true without
		// any tag having to be rewritten at the hand-over — and it is why the automatic → manual →
		// automatic round trip is safe. The credentials still go: Odigos no longer configures this
		// Collector, and nothing should keep pulling a configuration that carries the destination's
		// credential.
		out.Problems = append(out.Problems, collectorstatus.Problem{
			Code:     ProblemTeardownManualMode,
			Severity: collectorstatus.SeverityInfo,
			Message: fmt.Sprintf("this Collector's deployment is %s, so Odigos deletes nothing on network %s: "+
				"the infrastructure is yours. Its credentials are revoked, and it keeps running and "+
				"exporting under its last configuration until you remove it", in.Mode, in.NetworkID),
		})
		revoked, err := revokeEveryLiveCredential(ctx, store, record, in.Reason)
		out.RevokedCredentials = revoked
		if err != nil {
			out.Problems = append(out.Problems, revocationFailed(in.Key, err))
			return out, err
		}
		return out, nil
	}

	// Credentials first, and nothing after them if they did not go.
	revoked, err := revokeEveryLiveCredential(ctx, store, record, in.Reason)
	out.RevokedCredentials = revoked
	if err != nil {
		out.Problems = append(out.Problems, revocationFailed(in.Key, err))
		return out, err
	}

	if in.Reason == collectorcredential.ReasonCapabilityRemoved {
		// PlanTeardown's other half: revoked, not deleted. The Collector keeps running on its last
		// configuration and is reported as such.
		out.Problems = append(out.Problems, collectorstatus.Problem{
			Code:     ProblemTeardownCapabilityRemoved,
			Severity: collectorstatus.SeverityDegraded,
			Message: "the managed-Collector capability was removed, so this Collector's credentials are " +
				"revoked and Odigos no longer configures it. It is NOT deleted: it keeps running and " +
				"exporting under its last configuration until you remove it",
		})
		return out, nil
	}

	others := othersThan(ctx, store, in.Key)

	// Per network, in dependency order.
	steps := []func(context.Context, TeardownClients, teardownScope, *TeardownResult) error{
		teardownScalableTarget,
		teardownService,
		teardownLoadBalancer,
		teardownTargetGroups,
		teardownSecurityGroup,
		teardownNetworkSecrets,
		// Shared last, because "no network still references this" is only true once this network's own
		// objects are gone. The cluster is only reported as preserved: it is always customer-owned.
		teardownSharedSecret,
		preserveCluster,
		teardownLogGroup,
	}
	scope := teardownScope{in: in, record: record, others: others}
	var failures []error
	for _, step := range steps {
		if err := step(ctx, clients, scope, &out); err != nil {
			// Collected rather than returned: a security group AWS will not release yet must not stop the
			// secrets from being deleted, and the row stays either way.
			failures = append(failures, err)
		}
	}

	if len(failures) > 0 {
		retryOnly := true
		for _, failure := range failures {
			if !errors.Is(failure, baseapi.ErrCollectorTeardownRetry) {
				retryOnly = false
				break
			}
		}
		joined := errors.Join(failures...)
		if retryOnly {
			out.Problems = append(out.Problems, collectorstatus.Problem{
				Code: ProblemTeardownIncomplete, Severity: collectorstatus.SeverityPending,
				Message: "Cloud teardown is waiting for AWS to release dependent network interfaces; the connector will retry.",
			})
			return out, joined
		}
		out.Problems = append(out.Problems, collectorstatus.Problem{
			Code:     ProblemTeardownIncomplete,
			Severity: collectorstatus.SeverityError,
			Message: fmt.Sprintf("the Collector on %s is not fully removed yet and its record is kept: %v",
				in.NetworkID, joined),
		})
		// A mixed failure must not inherit the retry marker from one AWS dependency error.
		return out, fmt.Errorf("Collector teardown failed: %v", joined)
	}

	// The row survives an object Odigos created and could not remove, and this is the one case where
	// finishing the teardown would be worse than not finishing it. Deleting the row would take the second
	// half of the deletion gate with it, so the object becomes infrastructure nothing records and nobody
	// owns — and the row is also the surface the retention is REPORTED on, so it would go quiet at the
	// same moment. Kept instead, and reported, until a tag scan re-records the ref or somebody removes
	// the object; the platform's own cleanup timeout is what stops this holding a delete for ever.
	if leaked := leakedObjects(out.Kept); len(leaked) > 0 {
		out.Problems = append(out.Problems, collectorstatus.Problem{
			Code:     ProblemTeardownIncomplete,
			Severity: collectorstatus.SeverityError,
			Message: fmt.Sprintf("the Collector record for %s is kept because %s carries Odigos's ownership "+
				"tag and is not recorded here, so Odigos created it and cannot remove it. Deleting the record "+
				"would leave it with no owner at all: re-record it by letting a converge pass adopt it, or "+
				"remove it yourself", in.NetworkID, strings.Join(leaked, ", ")),
		})
		return out, nil
	}

	// The row last. Everything deletable is gone, so the refs no longer authorize anything — and the
	// objects that are still standing are the ones no record could have authorized deleting anyway.
	if err := store.DeleteManagedCollector(ctx, in.Key); err != nil {
		return out, fmt.Errorf("deleting the Collector record for %s: %w", in.Key.Region, err)
	}
	out.RecordRemoved = true
	log.Info("collector teardown complete",
		"network", in.NetworkID, "region", in.Key.Region,
		"removed", len(out.Removed), "kept", len(out.Kept), "credentials_revoked", out.RevokedCredentials)
	return out, nil
}

// leakedObjects names the objects that keep a Collector's record alive.
func leakedObjects(kept []KeptObject) []string {
	var out []string
	for _, k := range kept {
		if k.Leaked {
			out = append(out, k.Kind+" "+k.Ref)
		}
	}
	return out
}

// teardownScope is what every step needs: the request, the row, this connector's identity as the row
// records it, and the other networks still using the shared objects.
type teardownScope struct {
	in     TeardownInput
	record basestore.CollectorRecord
	others []basestore.CollectorRecord
}

// revokeEveryLiveCredential revokes every credential the gateway would still accept for this Collector.
//
// EVERY live one, not the active one: a teardown that lands during a rotation finds an active credential
// and a superseded one, and the superseded one is still accepted for the length of the old tasks' drain
// . Leaving it would mean the one credential still able to open a session is the one nobody was
// tracking.
//
// A Collector with no identity — a row that never got as far as issuing — has nothing to revoke, and
// that is a success rather than a skip: there is no session anything could hold.
func revokeEveryLiveCredential(ctx context.Context, store TeardownStore, record basestore.CollectorRecord, reason collectorcredential.Reason) (int, error) {
	if record.CollectorID == "" {
		return 0, nil
	}
	if reason == "" {
		reason = collectorcredential.ReasonTeardown
	}
	revoked, err := store.RevokeCollectorCredentials(ctx, record.CollectorID, reason, collectorcredential.ActorConnector)
	if err != nil {
		// The count of what DID go is returned alongside the error: a partial revocation still narrowed
		// the window, and the caller reports both.
		return len(revoked), fmt.Errorf("revoking the credentials of Collector %s: %w", record.CollectorID, err)
	}
	return len(revoked), nil
}

func revocationFailed(key basestore.CollectorKey, err error) collectorstatus.Problem {
	return collectorstatus.Problem{
		Code:     ProblemTeardownRevocationFailed,
		Severity: collectorstatus.SeverityError,
		Message: fmt.Sprintf("nothing was deleted for the Collector on %s: its credentials could not be "+
			"revoked (%v). A task that outlives the delete call would keep an authenticated session open "+
			"and keep being handed the destination's credential, so the deletion waits for the "+
			"revocation rather than racing it", key.Region, err),
	}
}

// othersThan is every OTHER network this connector records.
//
// A read failure yields NO others, which keeps every shared object — the safe direction. Answering "no
// network needs the cluster" from a failed read is how a region's whole Collector fleet loses the
// cluster it runs in.
func othersThan(ctx context.Context, store TeardownStore, key basestore.CollectorKey) []basestore.CollectorRecord {
	all, err := store.ListManagedCollectors(ctx)
	if err != nil {
		log.Warn("could not list the other Collectors; every shared object is kept",
			"region", key.Region, "error", err)
		return []basestore.CollectorRecord{{Key: basestore.CollectorKey{Region: key.Region}, NetworkID: "unknown"}}
	}
	var others []basestore.CollectorRecord
	for _, rec := range all {
		if rec.Key == key {
			continue
		}
		others = append(others, rec)
	}
	return others
}

// gate is the ONE decision every step goes through, and the only place a delete is authorized.
//
// It records what it decided on the result either way: an allowed object is named in Removed by the
// step that removes it, and a refused one is named in Kept with the reason, so a teardown's report is
// the complete inventory of what it touched and what it did not.
//
// The two independent records requires are both arguments: `tags` is what the object itself says,
// and `recorded` is what this connector's own state says. Neither is sufficient — a tag a customer
// pasted into their own template refuses, and a ref left in a stale row refuses.
func (s teardownScope) gate(kind, ref string, tags baseapi.CollectorTags, customerNamed, recorded bool, out *TeardownResult) bool {
	object := collectorownership.Object{
		Ref:           ref,
		Tags:          tags,
		CustomerNamed: customerNamed,
		Recorded:      recorded,
	}
	if collectorownership.MayDelete(object, s.in.Identity, s.in.Mode) {
		return true
	}

	class := collectorownership.Classify(object, s.in.Identity, s.in.Mode)
	reason, severity := keepReason(class, customerNamed, recorded)
	// Odigos created it and cannot remove it. See KeptObject.Leaked.
	leaked := !customerNamed && !recorded &&
		class.Origin == collectorownership.OriginOdigos && class.Attribution.Mine()
	out.Kept = append(out.Kept, KeptObject{Kind: kind, Ref: ref, Reason: reason, Leaked: leaked})
	out.Problems = append(out.Problems, collectorstatus.Problem{
		Code:     ProblemTeardownRetained,
		Severity: severity,
		Message: fmt.Sprintf("the %s %s is left standing: %s. It is still in your account and Odigos no "+
			"longer manages it", kind, ref, reason),
	})
	return false
}

// keepReason says why an object survived teardown, and how loudly to say it.
//
// The severity is the interesting half, because the two cases look identical in the infrastructure and
// mean opposite things:
//
//   - BY DESIGN (Info): Odigos never created this. A cluster the customer pointed it at, or a stack
//     applied from a published artifact and adopted for convergence. Nothing is wrong, nothing needs
//     doing, and reporting it as a fault would put a standing Degraded on every teardown of every
//     adopted Collector — which is how Degraded stops meaning anything.
//   - UNEXPECTED (Degraded): the object looks like something Odigos created and one of the two records
//     disagrees. That is the state that leaks: infrastructure nobody is converging and nothing may
//     remove, which only a tag scan or a hand deletion resolves.
func keepReason(class collectorownership.Class, customerNamed, recorded bool) (string, collectorstatus.Severity) {
	switch {
	case customerNamed:
		return "you pointed Odigos at this object through collector.regions.<region>, so Odigos placed its " +
			"own objects inside it and never owned it", collectorstatus.SeverityInfo
	case class.Attribution == collectorownership.AttributionForeign:
		return "its tags name a different connector", collectorstatus.SeverityDegraded
	case class.Attribution == collectorownership.AttributionNone,
		class.Attribution == collectorownership.AttributionUnattributed:
		return "it carries no tags saying this connector created it, so Odigos has no record of creating " +
			"it and will not remove it", collectorstatus.SeverityDegraded
	case class.Origin == collectorownership.OriginCustomer:
		return "you created it — from a published Odigos artifact or by hand — and Odigos converged it " +
			"without ever acquiring the right to delete it", collectorstatus.SeverityInfo
	case !recorded:
		return "this connector's own state does not record its ref, so only one of the two records " +
			"deletion needs agrees; a tag alone is not enough", collectorstatus.SeverityDegraded
	default:
		return fmt.Sprintf("the deletion gate refused it (%s, %s, %s mode)",
			class.Attribution, class.Origin, class.Mode), collectorstatus.SeverityDegraded
	}
}

// removed records a deletion, and is the only thing that writes Removed.
func (out *TeardownResult) removed(kind, ref string) {
	out.Removed = append(out.Removed, kind+" "+ref)
	log.Info("collector teardown removed an object", "kind", kind, "ref", ref)
}

// keptShared records a shared object another network still needs.
//
// Its own function rather than a gate outcome, because the reason is not an ownership decision: the
// object may be perfectly deletable and simply still in use. Reported as Info — this is the steady
// state of tearing down one network out of several, not a condition.
func (out *TeardownResult) keptShared(kind, ref string, users []string) {
	reason := fmt.Sprintf("%d other network(s) still use it (%s)", len(users), strings.Join(users, ", "))
	out.Kept = append(out.Kept, KeptObject{Kind: kind, Ref: ref, Reason: reason})
	out.Problems = append(out.Problems, collectorstatus.Problem{
		Code:     ProblemTeardownSharedRetained,
		Severity: collectorstatus.SeverityInfo,
		Message: fmt.Sprintf("the %s %s is kept because %s. It is removed with the last Collector that "+
			"uses it", kind, ref, reason),
	})
}

// teardownScalableTarget deregisters the Application Auto Scaling target, which takes its policies with
// it.
//
// FIRST, before the service it addresses, and the ordering is not cosmetic: a target left registered
// against a deleted service is an object Application Auto Scaling keeps and Odigos can no longer address
// by the composed resource id, because that id names a service that no longer exists.
//
// The target is not in managed_collectors — there is no column for it, and there does not need to be.
// Its resource id is DERIVED from the row's own primary key (`service/<cluster>/<service>`), so the row
// existing for this network IS the record that Odigos registered it; what still has to be true is that
// the TAGS on the target say Odigos put it there, which is the half a customer could not fake.
func teardownScalableTarget(ctx context.Context, clients TeardownClients, scope teardownScope, out *TeardownResult) error {
	if scope.record.RuntimeRef == "" {
		// No cluster recorded, so no resource id can be composed. Nothing was ever registered against a
		// service that was never created.
		return nil
	}
	api, err := clients.AutoScaling(ctx, scope.in.Key.Region)
	if err != nil {
		return fmt.Errorf("build an Application Auto Scaling client for %s: %w", scope.in.Key.Region, err)
	}

	resourceID := ScalableTargetResourceID(clusterNameOf(scope.record.RuntimeRef), scope.in.Key.Region)
	target, err := describeScalableTarget(ctx, api, resourceID)
	if err != nil {
		return err
	}
	if target == nil {
		return nil
	}

	// The same two reads the converge path makes, through the same functions: the target, then its tags
	// through their own call because a describe returns none. A teardown that read ownership its own way
	// would be free to disagree with the converge about whose target this is.
	object, err := scalableTargetObject(ctx, api, target, scope.derivedNameRecorded())
	if err != nil {
		return err
	}
	if !scope.gate("Application Auto Scaling target", resourceID, object.Tags, false, object.Recorded, out) {
		return nil
	}

	if _, err := api.DeregisterScalableTarget(ctx, &aas.DeregisterScalableTargetInput{
		ServiceNamespace:  aastypes.ServiceNamespaceEcs,
		ResourceId:        aws.String(resourceID),
		ScalableDimension: aastypes.ScalableDimensionECSServiceDesiredCount,
	}); err != nil && !isScalableTargetNotFound(err) {
		return fmt.Errorf("deregister the scalable target %s: %w", resourceID, err)
	}
	out.removed("Application Auto Scaling target", resourceID)
	return nil
}

// teardownService deletes the ECS service and stops any of its tasks ECS leaves running.
//
// Looked up by NAME rather than only by the recorded ARN, and it is the one object treated that way. A
// service under this name with no recorded ref is a Collector that is still RUNNING — still holding the
// destination's credential, still costing money, still receiving telemetry — which is a materially
// different kind of leak from an unrecorded security group, and the one thing a teardown must not leave
// unsaid. The gate still refuses to delete it; what the lookup buys is that it is reported.
//
// Force, because a service with running tasks cannot be deleted otherwise. DeleteService changes it to
// DRAINING, which the converge lookup intentionally hides. Teardown must still find that service on
// every retry: ECS can leave its tasks RUNNING with desiredCount=0, and their ENIs hold the Collector
// security group indefinitely. StopTask is safe only after the service passed the ownership gate.
func teardownService(ctx context.Context, clients TeardownClients, scope teardownScope, out *TeardownResult) error {
	if scope.record.RuntimeRef == "" {
		return nil
	}
	api, err := clients.ECS(ctx, scope.in.Key.Region)
	if err != nil {
		return fmt.Errorf("build an ECS client for %s: %w", scope.in.Key.Region, err)
	}

	name := ServiceName(scope.in.Key.Region)
	found, err := describeTeardownService(ctx, api, scope.record.RuntimeRef, name)
	if err != nil {
		return err
	}
	if found == nil {
		return nil
	}

	arn := aws.ToString(found.ServiceArn)
	if !scope.gate("ECS service", arn, baseapi.ParseCollectorTags(ecsTagMap(found.Tags)), false,
		scope.record.Recorded(arn), out) {
		return nil
	}

	if aws.ToString(found.Status) == serviceStatusActive {
		if _, err := api.DeleteService(ctx, &ecs.DeleteServiceInput{
			Cluster: aws.String(scope.record.RuntimeRef),
			Service: aws.String(name),
			Force:   aws.Bool(true),
		}); err != nil && !isServiceNotFound(err) {
			return fmt.Errorf("delete the ECS service %s: %w", name, err)
		}
	}
	if err := stopCollectorServiceTasks(ctx, api, scope.record.RuntimeRef, name); err != nil {
		return err
	}
	remaining, err := describeTeardownService(ctx, api, scope.record.RuntimeRef, name)
	if err != nil {
		return err
	}
	if remaining != nil {
		return fmt.Errorf("%w: ECS service %s is %s; waiting for its tasks and network interfaces to drain", baseapi.ErrCollectorTeardownRetry, name, aws.ToString(remaining.Status))
	}
	out.removed("ECS service", arn)
	return nil
}

// describeTeardownService also returns DRAINING services. The ordinary describeService deliberately
// returns ACTIVE only because a draining service cannot be converged back into service.
func describeTeardownService(ctx context.Context, api ECSTeardownClient, cluster, name string) (*ecstypes.Service, error) {
	result, err := api.DescribeServices(ctx, &ecs.DescribeServicesInput{
		Cluster: aws.String(cluster), Services: []string{name}, Include: []ecstypes.ServiceField{ecstypes.ServiceFieldTags},
	})
	if err != nil {
		return nil, fmt.Errorf("describe ECS service %s for teardown: %w", name, err)
	}
	for i := range result.Services {
		service := &result.Services[i]
		if aws.ToString(service.ServiceName) == name && aws.ToString(service.Status) != "INACTIVE" {
			return service, nil
		}
	}
	return nil, nil
}

func stopCollectorServiceTasks(ctx context.Context, api ECSTeardownClient, cluster, name string) error {
	paginator := ecs.NewListTasksPaginator(api, &ecs.ListTasksInput{
		Cluster: aws.String(cluster), ServiceName: aws.String(name), DesiredStatus: ecstypes.DesiredStatusRunning,
	})
	var tasks []string
	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return fmt.Errorf("list running tasks of Collector service %s: %w", name, err)
		}
		tasks = append(tasks, page.TaskArns...)
	}
	var failures []error
	for _, task := range tasks {
		if _, err := api.StopTask(ctx, &ecs.StopTaskInput{
			Cluster: aws.String(cluster), Task: aws.String(task), Reason: aws.String("Odigos managed Collector teardown"),
		}); err != nil {
			failures = append(failures, fmt.Errorf("stop Collector task %s: %w", task, err))
		}
	}
	return errors.Join(failures...)
}

// teardownLoadBalancer deletes the network load balancer, and with it its listeners.
func teardownLoadBalancer(ctx context.Context, clients TeardownClients, scope teardownScope, out *TeardownResult) error {
	api, err := clients.LoadBalancers(ctx, scope.in.Key.Region)
	if err != nil {
		return fmt.Errorf("build an Elastic Load Balancing client for %s: %w", scope.in.Key.Region, err)
	}

	// By NAME rather than by the recorded ref, like every other per-network object here. The recorded ref
	// is what the GATE reads; FINDING the object is a different question, and answering it from the row
	// alone would let a load balancer created by a pass that died before recording its ref disappear
	// silently — teardown would delete the row, and with it the only thing that could ever authorize
	// removing that load balancer.
	found, err := describeLoadBalancer(ctx, api, LoadBalancerName(scope.in.NetworkID))
	if err != nil {
		return err
	}
	if found == nil {
		// Either nothing was created, or `endpointOverride` put the customer's own front in front of the
		// Collector — in which case Odigos created no load balancer to remove.
		return nil
	}
	ref := aws.ToString(found.LoadBalancerArn)

	object, err := taggedObject(ctx, api, ref)
	if err != nil {
		return err
	}
	if !scope.gate("load balancer", ref, object.Tags, false, scope.record.Recorded(ref), out) {
		return nil
	}
	if _, err := api.DeleteLoadBalancer(ctx, &elb.DeleteLoadBalancerInput{
		LoadBalancerArn: aws.String(ref),
	}); err != nil && !isLoadBalancerNotFound(err) {
		return fmt.Errorf("delete the load balancer %s: %w", ref, err)
	}
	out.removed("load balancer", ref)
	return nil
}

// teardownTargetGroups deletes the two data-port target groups.
//
// After the load balancer, because a target group a listener still forwards to cannot be deleted — and
// the load balancer's deletion is what removes the listeners. AWS releases the association
// asynchronously, so a target group refused with ResourceInUse on the pass that deleted the load
// balancer is expected: it is reported, the row is kept, and the next pass removes it.
func teardownTargetGroups(ctx context.Context, clients TeardownClients, scope teardownScope, out *TeardownResult) error {
	api, err := clients.LoadBalancers(ctx, scope.in.Key.Region)
	if err != nil {
		return fmt.Errorf("build an Elastic Load Balancing client for %s: %w", scope.in.Key.Region, err)
	}

	var failures []error
	// The data ports Odigos names groups for, rather than the recorded refs — the same reason as the load
	// balancer above, and it additionally covers a pass that created the second group and died before
	// recording either.
	for _, port := range collectoringress.DataPorts() {
		found, err := describeTargetGroup(ctx, api, TargetGroupName(scope.in.NetworkID, port))
		if err != nil {
			failures = append(failures, err)
			continue
		}
		if found == nil {
			continue
		}
		ref := aws.ToString(found.TargetGroupArn)
		object, err := taggedObject(ctx, api, ref)
		if err != nil {
			failures = append(failures, err)
			continue
		}
		if !scope.gate("target group", ref, object.Tags, false, scope.record.Recorded(ref), out) {
			continue
		}
		if _, err := api.DeleteTargetGroup(ctx, &elb.DeleteTargetGroupInput{
			TargetGroupArn: aws.String(ref),
		}); err != nil && !isTargetGroupNotFound(err) {
			failures = append(failures, fmt.Errorf("delete the target group %s: %w", ref, err))
			continue
		}
		out.removed("target group", ref)
	}
	return errors.Join(failures...)
}

// teardownSecurityGroup deletes the Collector's own security group.
//
// Last of the network objects, because it is attached to the load balancer AND to the tasks, and EC2
// refuses to delete a group any network interface still references. Both of those are asynchronous:
// a load balancer takes minutes to release its interfaces, so a DependencyViolation here on the pass
// that deleted it is the expected outcome rather than a fault — reported, row kept, retried.
//
// A group the customer supplied through `securityGroupIds` is never touched: theirs REPLACE Odigos's on
// the tasks, and deleting a group a customer chose would take out whatever else uses it.
func teardownSecurityGroup(ctx context.Context, clients TeardownClients, scope teardownScope, out *TeardownResult) error {
	ref := scope.record.IngressRef
	if ref == "" {
		return nil
	}
	api, err := clients.EC2(ctx, scope.in.Key.Region)
	if err != nil {
		return fmt.Errorf("build an EC2 client for %s: %w", scope.in.Key.Region, err)
	}

	// By NAME and VPC, the way the converge finds it, rather than by the recorded id — so a group left
	// by a pass that created it and died before recording the ref is found rather than orphaned. The
	// recorded id is still what the gate reads: finding an object and being allowed to delete it are two
	// different questions.
	found, err := describeSecurityGroup(ctx, api, SecurityGroupName(scope.in.NetworkID), scope.in.NetworkID)
	if err != nil {
		return err
	}
	if found == nil {
		return nil
	}
	id := aws.ToString(found.GroupId)
	customerNamed := containsStringValue(scope.in.CustomerSecurityGroupIDs, id)
	if !scope.gate("security group", id, baseapi.ParseCollectorTags(ec2TagMap(found.Tags)),
		customerNamed, scope.record.Recorded(id), out) {
		return nil
	}

	if _, err := api.DeleteSecurityGroup(ctx, &ec2.DeleteSecurityGroupInput{
		GroupId: aws.String(id),
	}); err != nil && !isSecurityGroupNotFound(err) {
		var awsErr smithy.APIError
		if errors.As(err, &awsErr) && awsErr.ErrorCode() == "DependencyViolation" {
			return fmt.Errorf("%w: delete security group %s: %v", baseapi.ErrCollectorTeardownRetry, id, err)
		}
		return fmt.Errorf("delete the security group %s: %w", id, err)
	}
	out.removed("security group", id)
	return nil
}

// teardownNetworkSecrets deletes the two secrets that belong to this Collector alone: its OpAMP
// credential and its cold-start bootstrap configuration.
//
// Both are FORCE-deleted, with no recovery window, and that is a decision with a cost either way. A
// scheduled deletion reserves the NAME for up to 30 days, so a customer who tore a Collector down and
// enabled it again a week later would find `EnsureSecret` restoring a secret whose credential was
// revoked at teardown — recoverable, because that path exists, but only after a pass that reports a
// Collector unable to connect. Forcing means the values are gone immediately, which is the right answer
// for these two: one holds a credential that was just revoked and the other a configuration that is
// re-rendered from the destinations on the next pass. Neither is worth 30 days of recoverability, and
// both hold material nobody should be able to restore.
func teardownNetworkSecrets(ctx context.Context, clients TeardownClients, scope teardownScope, out *TeardownResult) error {
	api, err := clients.Secrets(ctx, scope.in.Key.Region)
	if err != nil {
		return fmt.Errorf("build a Secrets Manager client for %s: %w", scope.in.Key.Region, err)
	}

	connector := scope.in.Identity.ConnectorName
	names := []string{
		CredentialSecretName(connector, scope.in.Key.Region),
		BootstrapSecretName(connector, scope.in.Key.Region),
	}
	var failures []error
	for _, name := range names {
		if err := deleteSecret(ctx, api, scope, name, out); err != nil {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}

// teardownSharedSecret deletes the region's licence secret, which every Collector in the region shares.
func teardownSharedSecret(ctx context.Context, clients TeardownClients, scope teardownScope, out *TeardownResult) error {
	name := LicenseSecretName(scope.in.Identity.ConnectorName, scope.in.Key.Region)
	if users := scope.usersInRegion(); len(users) > 0 {
		out.keptShared("Secrets Manager secret", name, users)
		return nil
	}
	api, err := clients.Secrets(ctx, scope.in.Key.Region)
	if err != nil {
		return fmt.Errorf("build a Secrets Manager client for %s: %w", scope.in.Key.Region, err)
	}
	return deleteSecret(ctx, api, scope, name, out)
}

// deleteSecret is the gate and the delete for one secret, by name.
func deleteSecret(ctx context.Context, api SecretTeardownClient, scope teardownScope, name string, out *TeardownResult) error {
	described, err := api.DescribeSecret(ctx, &secretsmanager.DescribeSecretInput{SecretId: aws.String(name)})
	switch {
	case isSecretNotFound(err):
		return nil
	case err != nil:
		return fmt.Errorf("describe secret %s: %w", name, err)
	}
	if described.DeletedDate != nil {
		// Already on its way out from an earlier pass. Not re-deleted: a second DeleteSecret on a
		// secret already scheduled would be the one call that could turn a recoverable deletion into an
		// unrecoverable one behind the back of whoever scheduled it.
		return nil
	}

	arn := aws.ToString(described.ARN)
	if !scope.gate("Secrets Manager secret", name,
		baseapi.ParseCollectorTags(describedTags(described)), false, scope.secretRecorded(name), out) {
		return nil
	}
	if _, err := api.DeleteSecret(ctx, &secretsmanager.DeleteSecretInput{
		SecretId:                   aws.String(arn),
		ForceDeleteWithoutRecovery: aws.Bool(true),
	}); err != nil && !isSecretNotFound(err) {
		return fmt.Errorf("delete secret %s: %w", name, err)
	}
	out.removed("Secrets Manager secret", name)
	return nil
}

// preserveCluster records the customer-owned cluster as intentionally retained. Automatic Collector
// deployment never creates, changes, or deletes ECS clusters, so teardown needs no cluster API call.
func preserveCluster(_ context.Context, _ TeardownClients, scope teardownScope, out *TeardownResult) error {
	ref := scope.record.RuntimeRef
	if ref == "" {
		return nil
	}
	out.Kept = append(out.Kept, KeptObject{
		Kind:   "ECS cluster",
		Ref:    ref,
		Reason: "customer-provisioned through collector.regions.<region>.provider.clusterArn; Odigos only removes its service",
	})
	return nil
}

// teardownLogGroup deletes the region's shared log group.
//
// LAST, and the ordering is for the reader rather than for AWS: the log group holds the Collector's own
// stdout, which is the only account of why it was unhealthy — so it outlives every object whose removal
// somebody might want to explain afterwards. Deleting it does destroy those logs, which is what
// asks for: an object Odigos created and left behind is a leak, and a log group with no Collector
// writing to it is a bill with no purpose. The report names it so the removal is not a surprise.
func teardownLogGroup(ctx context.Context, clients TeardownClients, scope teardownScope, out *TeardownResult) error {
	name := LogGroupName(scope.in.Identity.ConnectorName)
	if users := scope.usersInRegion(); len(users) > 0 {
		out.keptShared("log group", name, users)
		return nil
	}
	if scope.in.Partition == "" || scope.in.AccountID == "" {
		// The tags are read by ARN, and the ARN cannot be composed without both. Refused rather than
		// guessed: an ARN with an empty account matches nothing, and ListTagsForResource would answer
		// "no tags" — which reads exactly like a group Odigos did not create and would delete nothing,
		// silently, for ever.
		out.Kept = append(out.Kept, KeptObject{
			Kind: "log group", Ref: name,
			Reason: "its ARN could not be composed, so its ownership tags could not be read",
		})
		return nil
	}
	api, err := clients.Logs(ctx, scope.in.Key.Region)
	if err != nil {
		return fmt.Errorf("build a CloudWatch Logs client for %s: %w", scope.in.Key.Region, err)
	}

	arn := logGroupARN(scope.in.Partition, scope.in.Key.Region, scope.in.AccountID, name)
	tags, err := api.ListTagsForResource(ctx, &cloudwatchlogs.ListTagsForResourceInput{ResourceArn: aws.String(arn)})
	if err != nil {
		if isLogGroupNotFound(err) {
			return nil
		}
		return fmt.Errorf("read the tags on log group %s: %w", name, err)
	}
	if !scope.gate("log group", name, baseapi.ParseCollectorTags(tags.Tags), false,
		scope.derivedNameRecorded(), out) {
		return nil
	}
	if _, err := api.DeleteLogGroup(ctx, &cloudwatchlogs.DeleteLogGroupInput{
		LogGroupName: aws.String(name),
	}); err != nil && !isLogGroupNotFound(err) {
		return fmt.Errorf("delete the log group %s: %w", name, err)
	}
	out.removed("log group", name)
	return nil
}

// The reader interfaces the converge path and this one share.
//
// They exist so ONE describe serves both: `describeService`, `describeSecurityGroup`, `taggedObject`,
// `describeScalableTarget` and `scalableTargetObject` take
// these rather than the full client interfaces, so teardown reads an object's ownership through exactly
// the function the converge reads it through. A second implementation of "whose target is this" is the
// disagreement warns about, expressed as duplicate code.
type (
	serviceReader interface {
		DescribeServices(ctx context.Context, in *ecs.DescribeServicesInput, opts ...func(*ecs.Options)) (*ecs.DescribeServicesOutput, error)
	}
	securityGroupReader interface {
		DescribeSecurityGroups(ctx context.Context, in *ec2.DescribeSecurityGroupsInput, opts ...func(*ec2.Options)) (*ec2.DescribeSecurityGroupsOutput, error)
	}
	elbTagReader interface {
		DescribeTags(ctx context.Context, in *elb.DescribeTagsInput, opts ...func(*elb.Options)) (*elb.DescribeTagsOutput, error)
	}
	loadBalancerReader interface {
		DescribeLoadBalancers(ctx context.Context, in *elb.DescribeLoadBalancersInput, opts ...func(*elb.Options)) (*elb.DescribeLoadBalancersOutput, error)
	}
	targetGroupReader interface {
		DescribeTargetGroups(ctx context.Context, in *elb.DescribeTargetGroupsInput, opts ...func(*elb.Options)) (*elb.DescribeTargetGroupsOutput, error)
	}
	scalableTargetReader interface {
		DescribeScalableTargets(ctx context.Context, in *aas.DescribeScalableTargetsInput, opts ...func(*aas.Options)) (*aas.DescribeScalableTargetsOutput, error)
		ListTagsForResource(ctx context.Context, in *aas.ListTagsForResourceInput, opts ...func(*aas.Options)) (*aas.ListTagsForResourceOutput, error)
	}
)

// usersInRegion names the other networks in this region that still need its shared objects — the
// licence secret and the log group.
func (s teardownScope) usersInRegion() []string {
	var users []string
	for _, other := range s.others {
		if other.Key.Region == s.in.Key.Region {
			users = append(users, other.NetworkID)
		}
	}
	sort.Strings(users)
	return users
}

// allUsers names every other network this connector records, for the connector-wide roles.
func (s teardownScope) allUsers() []string {
	var users []string
	for _, other := range s.others {
		users = append(users, other.Key.Region+"/"+other.NetworkID)
	}
	sort.Strings(users)
	return users
}

// secretRecorded and derivedNameRecorded are how the second half of the deletion gate is satisfied for
// an object with no ref column of its own.
//
// Most of a Collector's objects are recorded by REF, because their ids are opaque: an ECS service ARN,
// a load balancer ARN, a security group id. `CollectorRecord.Recorded` matches those, and a ref that is
// not in the row grants no delete authority.
//
// Bootstrap, the log group, and the scalable target have derived names and no dedicated ref column.
// The credential and licence secrets have refs, checkpointed as soon as each exists. Teardown still
// probes their names when a ref is missing so a crash between AWS creation and the database write
// leaves a visible retained row rather than a silent leak.
//
// The row is the second record for derived names. Opaque refs still require an exact database match.
func (s teardownScope) secretRecorded(name string) bool {
	if name == LicenseSecretName(s.in.Identity.ConnectorName, s.in.Key.Region) {
		return s.record.LicenseSecretRef != ""
	}
	return s.record.CredentialRef != ""
}

func (s teardownScope) derivedNameRecorded() bool { return true }

// clusterNameOf is the cluster's name from its ARN, for the one API that will not take the ARN.
//
// Application Auto Scaling addresses an ECS service as `service/<cluster NAME>/<service name>` and
// refuses the ARN every other call in this package passes around (see ScalableTargetResourceID). An ARN
// ends in `cluster/<name>`, so the last segment is the name — and a value that is already a bare name
// passes through unchanged, which is what makes this safe to call on either.
func clusterNameOf(clusterRef string) string {
	if i := strings.LastIndex(clusterRef, "/"); i >= 0 {
		return clusterRef[i+1:]
	}
	return clusterRef
}

// The "already gone" predicates. Every one of them makes a deletion idempotent: a teardown resumed
// after an interrupted pass must treat what the previous pass removed as success, or a network whose
// service went in pass one could never finish.
func isServiceNotFound(err error) bool {
	var e *ecstypes.ServiceNotFoundException
	return errors.As(err, &e)
}

func isClusterNotFound(err error) bool {
	var e *ecstypes.ClusterNotFoundException
	return errors.As(err, &e)
}

func isLoadBalancerNotFound(err error) bool {
	var e *elbtypes.LoadBalancerNotFoundException
	return errors.As(err, &e)
}

func isTargetGroupNotFound(err error) bool {
	var e *elbtypes.TargetGroupNotFoundException
	return errors.As(err, &e)
}

func isLogGroupNotFound(err error) bool {
	var e *logstypes.ResourceNotFoundException
	return errors.As(err, &e)
}

func isScalableTargetNotFound(err error) bool {
	var e *aastypes.ObjectNotFoundException
	return errors.As(err, &e)
}

// isSecurityGroupNotFound matches on the error CODE, because EC2 reports errors as codes on a generic
// APIError rather than as the distinct types every other service here uses.
//
// DependencyViolation is deliberately NOT treated as "already gone". A group AWS will not release
// because a load balancer's network interfaces still hold it is a group that still EXISTS, and reporting
// that pass as complete would delete the row — and with it the only record that authorizes deleting the
// group at all.
func isSecurityGroupNotFound(err error) bool {
	var api smithy.APIError
	return errors.As(err, &api) && api.ErrorCode() == "InvalidGroup.NotFound"
}
