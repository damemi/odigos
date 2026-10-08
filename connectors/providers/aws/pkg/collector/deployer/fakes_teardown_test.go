package deployer

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	aas "github.com/aws/aws-sdk-go-v2/service/applicationautoscaling"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"
	logstypes "github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs/types"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	elb "github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"
	smtypes "github.com/aws/aws-sdk-go-v2/service/secretsmanager/types"

	"github.com/odigos-io/odigos-enterprise/connectors/base/domain/collectorcredential"
	basestore "github.com/odigos-io/odigos-enterprise/connectors/base/store"
)

// The destructive half of the fakes, kept in its own file for the same reason the destructive CLIENTS
// are in their own interfaces: a reader looking for "what can delete a customer's object" finds one file
// on each side rather than a Delete method hidden among forty converge methods.
//
// Every delete records itself in the fake's existing `calls` slice, so a test can assert not only that
// the right things went but that a refused object was left completely untouched — which is the property
// turns on, and one that "the object is still there" alone does not prove.

// --- ECS ---

func (f *fakeECS) DeleteService(_ context.Context, in *ecs.DeleteServiceInput, _ ...func(*ecs.Options)) (*ecs.DeleteServiceOutput, error) {
	cluster, name := aws.ToString(in.Cluster), aws.ToString(in.Service)
	f.calls = append(f.calls, fmt.Sprintf("DeleteService:%s/force=%t", name, aws.ToBool(in.Force)))
	if f.deleteServiceErr != nil {
		return nil, f.deleteServiceErr
	}
	delete(f.services, serviceKey(cluster, name))
	return &ecs.DeleteServiceOutput{}, nil
}

// --- ELBv2 ---

func (f *fakeELB) DeleteLoadBalancer(_ context.Context, in *elb.DeleteLoadBalancerInput, _ ...func(*elb.Options)) (*elb.DeleteLoadBalancerOutput, error) {
	arn := aws.ToString(in.LoadBalancerArn)
	f.calls = append(f.calls, "DeleteLoadBalancer:"+arn)
	if f.deleteLBErr != nil {
		return nil, f.deleteLBErr
	}
	for name, lb := range f.lbs {
		if aws.ToString(lb.LoadBalancerArn) == arn {
			delete(f.lbs, name)
		}
	}
	return &elb.DeleteLoadBalancerOutput{}, nil
}

func (f *fakeELB) DeleteTargetGroup(_ context.Context, in *elb.DeleteTargetGroupInput, _ ...func(*elb.Options)) (*elb.DeleteTargetGroupOutput, error) {
	arn := aws.ToString(in.TargetGroupArn)
	f.calls = append(f.calls, "DeleteTargetGroup:"+arn)
	if f.deleteTGErr != nil {
		return nil, f.deleteTGErr
	}
	for name, tg := range f.targetGroups {
		if aws.ToString(tg.TargetGroupArn) == arn {
			delete(f.targetGroups, name)
		}
	}
	return &elb.DeleteTargetGroupOutput{}, nil
}

// --- EC2 ---

func (f *fakeEC2SG) DeleteSecurityGroup(_ context.Context, in *ec2.DeleteSecurityGroupInput, _ ...func(*ec2.Options)) (*ec2.DeleteSecurityGroupOutput, error) {
	id := aws.ToString(in.GroupId)
	f.calls = append(f.calls, "DeleteSecurityGroup:"+id)
	if f.deleteErr != nil {
		return nil, f.deleteErr
	}
	for key, g := range f.groups {
		if aws.ToString(g.GroupId) == id {
			delete(f.groups, key)
		}
	}
	return &ec2.DeleteSecurityGroupOutput{}, nil
}

// --- Secrets Manager ---

func (f *fakeSecrets) DeleteSecret(_ context.Context, in *secretsmanager.DeleteSecretInput, _ ...func(*secretsmanager.Options)) (*secretsmanager.DeleteSecretOutput, error) {
	id := aws.ToString(in.SecretId)
	name, secret := f.find(id)
	f.calls = append(f.calls, fmt.Sprintf("DeleteSecret:%s/force=%t", name, aws.ToBool(in.ForceDeleteWithoutRecovery)))
	if f.deleteErr != nil {
		return nil, f.deleteErr
	}
	if secret == nil {
		return nil, &smtypes.ResourceNotFoundException{}
	}
	if aws.ToBool(in.ForceDeleteWithoutRecovery) {
		delete(f.secrets, name)
		return &secretsmanager.DeleteSecretOutput{}, nil
	}
	// A scheduled deletion keeps the secret AND its name for the recovery window, which is exactly the
	// state EnsureSecret has to restore from.
	deleted := time.Now().UTC()
	secret.deletedDate = &deleted
	return &secretsmanager.DeleteSecretOutput{}, nil
}

// --- CloudWatch Logs ---

func (f *fakeLogs) withGroup(arn string, tags map[string]string) *fakeLogs {
	if f.groupTags == nil {
		f.groupTags = map[string]map[string]string{}
	}
	f.groupTags[arn] = tags
	return f
}

func (f *fakeLogs) ListTagsForResource(_ context.Context, in *cloudwatchlogs.ListTagsForResourceInput, _ ...func(*cloudwatchlogs.Options)) (*cloudwatchlogs.ListTagsForResourceOutput, error) {
	arn := aws.ToString(in.ResourceArn)
	f.calls = append(f.calls, "ListTagsForResource:"+arn)
	tags, ok := f.groupTags[arn]
	if !ok {
		return nil, &logstypes.ResourceNotFoundException{}
	}
	return &cloudwatchlogs.ListTagsForResourceOutput{Tags: tags}, nil
}

func (f *fakeLogs) DeleteLogGroup(_ context.Context, in *cloudwatchlogs.DeleteLogGroupInput, _ ...func(*cloudwatchlogs.Options)) (*cloudwatchlogs.DeleteLogGroupOutput, error) {
	name := aws.ToString(in.LogGroupName)
	f.calls = append(f.calls, "DeleteLogGroup:"+name)
	if f.deleteErr != nil {
		return nil, f.deleteErr
	}
	return &cloudwatchlogs.DeleteLogGroupOutput{}, nil
}

// --- the client set ---

type fakeTeardownClients struct {
	ecs     *fakeECS
	elb     *fakeELB
	ec2     *fakeEC2SG
	secrets *fakeSecrets
	logs    *fakeLogs
	scaling *fakeAutoScaling
	// clientErr, when set, is returned by every constructor, which is what an expired credential or an
	// unreachable region looks like from here.
	clientErr error
}

func newFakeTeardownClients() *fakeTeardownClients {
	return &fakeTeardownClients{
		ecs: newFakeECS(), elb: newFakeELB(), ec2: newFakeEC2SG(), secrets: newFakeSecrets(),
		logs: newFakeLogs(), scaling: newFakeAutoScaling(),
	}
}

func (f *fakeTeardownClients) Logs(context.Context, string) (LogGroupTeardownClient, error) {
	return f.logs, f.clientErr
}

func (f *fakeTeardownClients) Secrets(context.Context, string) (SecretTeardownClient, error) {
	return f.secrets, f.clientErr
}

func (f *fakeTeardownClients) EC2(context.Context, string) (SecurityGroupTeardownClient, error) {
	return f.ec2, f.clientErr
}

func (f *fakeTeardownClients) LoadBalancers(context.Context, string) (LoadBalancerTeardownClient, error) {
	return f.elb, f.clientErr
}

func (f *fakeTeardownClients) ECS(context.Context, string) (ECSTeardownClient, error) {
	return f.ecs, f.clientErr
}

func (f *fakeTeardownClients) AutoScaling(context.Context, string) (ScalingTeardownClient, error) {
	return f.scaling, f.clientErr
}

// calls is every mutating call every client received, so a test can assert an ORDER across services —
// which is where teardown's safety properties live: the revocation before the deletes, the scalable
// target before the service, the load balancer before its target groups.
func (f *fakeTeardownClients) calls() []string {
	var all []string
	all = append(all, f.scaling.calls...)
	all = append(all, f.ecs.calls...)
	all = append(all, f.elb.calls...)
	all = append(all, f.ec2.calls...)
	all = append(all, f.secrets.calls...)
	all = append(all, f.logs.calls...)
	return all
}

// deletes is every call that DESTROYS something, which is the list a teardown test actually cares about.
func (f *fakeTeardownClients) deletes() []string {
	var out []string
	for _, call := range f.calls() {
		switch {
		case hasPrefix(call, "Delete"), hasPrefix(call, "Deregister"):
			out = append(out, call)
		}
	}
	return out
}

func hasPrefix(s, prefix string) bool { return len(s) >= len(prefix) && s[:len(prefix)] == prefix }

// --- the store ---

// fakeTeardownStore adds the rows and revocations teardown needs to fakeStore, which already holds the
// credential half the converge path uses.
type fakeTeardownStore struct {
	*fakeStore
	revoked   []string
	revokeErr error
	deleteErr error
	listErr   error
	deleted   []basestore.CollectorKey
}

func newFakeTeardownStore() *fakeTeardownStore {
	return &fakeTeardownStore{fakeStore: newFakeStore()}
}

// withRecord seeds a managed_collectors row — the second half of every deletion gate.
func (f *fakeTeardownStore) withRecord(rec basestore.CollectorRecord) *fakeTeardownStore {
	copy := rec
	f.rows[rec.Key] = &copy
	return f
}

func (f *fakeTeardownStore) ListManagedCollectors(context.Context) ([]basestore.CollectorRecord, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	out := make([]basestore.CollectorRecord, 0, len(f.rows))
	for _, row := range f.rows {
		out = append(out, *row)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Key.Region != out[j].Key.Region {
			return out[i].Key.Region < out[j].Key.Region
		}
		return out[i].NetworkID < out[j].NetworkID
	})
	return out, nil
}

func (f *fakeTeardownStore) RevokeCollectorCredentials(_ context.Context, collectorID string,
	reason collectorcredential.Reason, actor collectorcredential.Actor) ([]collectorcredential.Record, error) {
	f.calls = append(f.calls, "RevokeCollectorCredentials:"+collectorID+"/"+string(reason)+"/"+string(actor))
	if f.revokeErr != nil {
		return nil, f.revokeErr
	}
	var out []collectorcredential.Record
	for key, rec := range f.credentials {
		if rec.Binding.CollectorID != collectorID || !rec.Live() {
			continue
		}
		rec.State = collectorcredential.StateRevoked
		f.credentials[key] = rec
		f.revoked = append(f.revoked, rec.CredentialID)
		out = append(out, *rec)
	}
	sort.Strings(f.revoked)
	return out, nil
}

func (f *fakeTeardownStore) DeleteManagedCollector(_ context.Context, key basestore.CollectorKey) error {
	f.calls = append(f.calls, "DeleteManagedCollector:"+key.Region)
	if f.deleteErr != nil {
		return f.deleteErr
	}
	f.deleted = append(f.deleted, key)
	delete(f.rows, key)
	return nil
}

// withCredentials seeds live credentials for a Collector — two of them is a teardown that landed during
// a rotation, which says must revoke both.
func (f *fakeTeardownStore) withCredentials(collectorID string, states ...collectorcredential.State) *fakeTeardownStore {
	for i, state := range states {
		id := fmt.Sprintf("cred-%d", i+1)
		f.credentials[collectorID+"/"+id] = &collectorcredential.Record{
			CredentialID: id,
			State:        state,
			Binding:      collectorcredential.Binding{CollectorID: collectorID},
		}
	}
	return f
}

var _ = aas.DeregisterScalableTargetInput{}

// serviceARN and arnOf are how a test learns the ARN a seeded object was given, so a record can be built
// that actually MATCHES it — which is the second half of every deletion gate, and a fixture that got it
// wrong would make a refusal look like a pass.
func (f *fakeECS) serviceARN(name string) string {
	for _, svc := range f.services {
		if aws.ToString(svc.ServiceName) == name {
			return aws.ToString(svc.ServiceArn)
		}
	}
	return ""
}

func (f *fakeELB) arnOf(name string) string {
	if lb, ok := f.lbs[name]; ok {
		return aws.ToString(lb.LoadBalancerArn)
	}
	if tg, ok := f.targetGroups[name]; ok {
		return aws.ToString(tg.TargetGroupArn)
	}
	return ""
}

// withGroupIn seeds a security group in a NAMED vpc. withGroup hardcodes the ingress fixtures' VPC, and
// teardown looks a group up by (name, network id) from the row's own key — so a fixture in the wrong VPC
// would make every refusal look like a pass.
func (f *fakeEC2SG) withGroupIn(vpc, name string, tags map[string]string) *fakeEC2SG {
	return f.withGroupInWithIngress(vpc, name, tags, nil)
}

// called reports whether any call with this prefix was made, mirroring fakeStore.called so the two read
// the same way in a test that asserts across both.
func (f *fakeECS) called(prefix string) bool { return anyPrefix(f.calls, prefix) }

func anyPrefix(calls []string, prefix string) bool {
	for _, call := range calls {
		if hasPrefix(call, prefix) {
			return true
		}
	}
	return false
}

// sink is a call log SHARED across fakes, so a test can assert an order that spans two AWS services.
//
// It exists for one property and it is the property parking turns on: the scalable target has to be
// deregistered before the replica count is written, and those are two different APIs. Comparing indices
// into each fake's own slice would compare two unrelated numbers and pass whatever the order was.
type sink struct{ calls []string }

func (s *sink) record(call string) {
	if s != nil {
		s.calls = append(s.calls, call)
	}
}

func (s *sink) indexOf(prefix string) int {
	for i, call := range s.calls {
		if hasPrefix(call, prefix) {
			return i
		}
	}
	return -1
}
