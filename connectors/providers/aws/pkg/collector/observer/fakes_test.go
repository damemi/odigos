package observer

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"
	elb "github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2"
	elbtypes "github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2/types"

	baseapi "github.com/odigos-io/odigos-enterprise/connectors/base/api"
	"github.com/odigos-io/odigos-enterprise/connectors/base/domain/collectorimage"
	"github.com/odigos-io/odigos-enterprise/connectors/base/domain/collectorownership"
	"github.com/odigos-io/odigos-enterprise/connectors/base/domain/collectorstatus"
	basestore "github.com/odigos-io/odigos-enterprise/connectors/base/store"
	"github.com/odigos-io/odigos-enterprise/connectors/providers/aws/pkg/collector/deployer"
	"github.com/odigos-io/odigos-enterprise/connectors/providers/aws/pkg/collector/placement"
	collectorsettings "github.com/odigos-io/odigos-enterprise/connectors/providers/aws/pkg/collector/settings"
)

const (
	testRegion    = "eu-west-1"
	testAccount   = "123456789012"
	testConnector = "prod"
	testUID       = "uid-1"
	testVPC       = "vpc-0abc123def456789a"
	testEndpoint  = "wss://central.example.com/v1/opamp"
	testDigest    = "sha256:1111111111111111111111111111111111111111111111111111111111111111"
	testLBDNS     = "odigos-col-0abc123def456789a-abc.elb.eu-west-1.amazonaws.com"
)

var testNow = time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)

func ago(d time.Duration) *time.Time { t := testNow.Add(-d); return &t }

func testIdentity() collectorownership.Identity {
	return collectorownership.Identity{ConnectorName: testConnector, ConnectorID: testUID}
}

func testKey() basestore.CollectorKey {
	return basestore.CollectorKey{Region: testRegion}
}

func testNetwork() baseapi.CollectorNetwork {
	return baseapi.CollectorNetwork{Region: testRegion, NetworkID: testVPC, Kind: "aws.fargate-collector"}
}

func testPin() collectorimage.Pin {
	return collectorimage.Pin{
		Digest:       testDigest,
		Tag:          "v1.2.3",
		Repositories: map[collectorimage.Cloud]string{collectorimage.CloudAWS: "ghcr.io/odigos-io/odigos-enterprise-collector"},
		Platforms:    []collectorimage.Arch{collectorimage.ArchAMD64},
	}
}

func ourTags() map[string]string {
	return baseapi.CollectorManagedTags(testConnector, testUID, testRegion, testVPC)
}

func serviceARN() string {
	return "arn:aws:ecs:" + testRegion + ":" + testAccount + ":service/" + deployer.ClusterName() + "/" +
		deployer.ServiceName(testRegion)
}

func revisionARN(revision int) string {
	return fmt.Sprintf("arn:aws:ecs:%s:%s:task-definition/%s:%d",
		testRegion, testAccount, deployer.TaskDefinitionFamily(testRegion), revision)
}

func targetGroupARN(port int) string {
	return fmt.Sprintf("arn:aws:elasticloadbalancing:%s:%s:targetgroup/%s/%d",
		testRegion, testAccount, deployer.TargetGroupName(testVPC, port), port)
}

const loadBalancerARN = "arn:aws:elasticloadbalancing:eu-west-1:123456789012:loadbalancer/net/odigos-col-x/1"

// --- ECS ---

// fakeECS is the read-only ECS surface. It answers a service ECS cannot find with a FAILURE rather than
// an error, and returns tags only when they were asked for — both the way the real API behaves, so code
// that mishandled either would fail here rather than pass.
type fakeECS struct {
	services map[string]*ecstypes.Service // by cluster ref + "/" + name
	families map[string]*ecstypes.TaskDefinition
	// tasks are the stopped tasks of a service, keyed by cluster + "/" + service.
	tasks map[string][]ecstypes.Task

	calls []string

	describeServiceErr    error
	describeRevisionErr   error
	listTasksErr          error
	describeTaskDefTagged bool
}

func newFakeECS() *fakeECS {
	return &fakeECS{
		services: map[string]*ecstypes.Service{},
		families: map[string]*ecstypes.TaskDefinition{},
		tasks:    map[string][]ecstypes.Task{},
	}
}

func serviceKey(cluster, name string) string { return cluster + "/" + name }

// withService installs a running Collector service: two tasks, both data ports attached, on the pinned
// image.
func (f *fakeECS) withService(mutate func(*ecstypes.Service)) *fakeECS {
	svc := &ecstypes.Service{
		ServiceName:    aws.String(deployer.ServiceName(testRegion)),
		ServiceArn:     aws.String(serviceARN()),
		ClusterArn:     aws.String("arn:aws:ecs:" + testRegion + ":" + testAccount + ":cluster/" + deployer.ClusterName()),
		Status:         aws.String("ACTIVE"),
		TaskDefinition: aws.String(revisionARN(1)),
		DesiredCount:   2,
		RunningCount:   2,
		Tags:           ecsTags(ourTags()),
		Deployments: []ecstypes.Deployment{{
			Status:       aws.String("PRIMARY"),
			RolloutState: ecstypes.DeploymentRolloutStateCompleted,
		}},
		NetworkConfiguration: &ecstypes.NetworkConfiguration{
			AwsvpcConfiguration: &ecstypes.AwsVpcConfiguration{
				Subnets:        []string{"subnet-a", "subnet-b"},
				SecurityGroups: []string{"sg-collector"},
			},
		},
		LoadBalancers: []ecstypes.LoadBalancer{
			{TargetGroupArn: aws.String(targetGroupARN(4317)), ContainerName: aws.String(deployer.CollectorContainerName), ContainerPort: aws.Int32(4317)},
			{TargetGroupArn: aws.String(targetGroupARN(4318)), ContainerName: aws.String(deployer.CollectorContainerName), ContainerPort: aws.Int32(4318)},
		},
	}
	if mutate != nil {
		mutate(svc)
	}
	f.services[serviceKey(deployer.ClusterName(), aws.ToString(svc.ServiceName))] = svc
	f.withRevision(aws.ToString(svc.TaskDefinition), nil)
	return f
}

// withRevision installs the task definition a service runs.
func (f *fakeECS) withRevision(arn string, mutate func(*ecstypes.TaskDefinition)) *fakeECS {
	td := &ecstypes.TaskDefinition{
		TaskDefinitionArn: aws.String(arn),
		Cpu:               aws.String("1024"),
		Memory:            aws.String("2048"),
		RuntimePlatform:   &ecstypes.RuntimePlatform{CpuArchitecture: ecstypes.CPUArchitectureX8664},
		ContainerDefinitions: []ecstypes.ContainerDefinition{{
			Name:  aws.String(deployer.CollectorContainerName),
			Image: aws.String("ghcr.io/odigos-io/odigos-enterprise-collector@" + testDigest),
			Environment: []ecstypes.KeyValuePair{
				{Name: aws.String("ODIGOS_OPAMP_ENDPOINT"), Value: aws.String(testEndpoint)},
				{Name: aws.String("ODIGOS_CLOUD_CONNECTOR_ID"), Value: aws.String(testUID)},
			},
		}},
	}
	if mutate != nil {
		mutate(td)
	}
	f.families[arn] = td
	return f
}

// withStoppedTask installs one stopped task on the Collector's service.
func (f *fakeECS) withStoppedTask(task ecstypes.Task) *fakeECS {
	key := serviceKey(deployer.ClusterName(), deployer.ServiceName(testRegion))
	if task.TaskArn == nil {
		task.TaskArn = aws.String(fmt.Sprintf("arn:aws:ecs:%s:%s:task/%d", testRegion, testAccount, len(f.tasks[key])+1))
	}
	f.tasks[key] = append(f.tasks[key], task)
	return f
}

func (f *fakeECS) DescribeServices(_ context.Context, in *ecs.DescribeServicesInput, _ ...func(*ecs.Options)) (*ecs.DescribeServicesOutput, error) {
	f.calls = append(f.calls, "DescribeServices:"+fmt.Sprint(in.Services))
	if f.describeServiceErr != nil {
		return nil, f.describeServiceErr
	}
	cluster := aws.ToString(in.Cluster)
	var out ecs.DescribeServicesOutput
	for _, name := range in.Services {
		svc, ok := f.services[serviceKey(cluster, name)]
		if !ok {
			out.Failures = append(out.Failures, ecstypes.Failure{Arn: aws.String(name), Reason: aws.String("MISSING")})
			continue
		}
		described := *svc
		if !hasServiceField(in.Include, ecstypes.ServiceFieldTags) {
			// Tags come back only when asked for. A fake that always returned them would hide a missing
			// Include, and the origin classification would look like it worked.
			described.Tags = nil
		}
		out.Services = append(out.Services, described)
	}
	return &out, nil
}

func hasServiceField(fields []ecstypes.ServiceField, want ecstypes.ServiceField) bool {
	for _, f := range fields {
		if f == want {
			return true
		}
	}
	return false
}

func (f *fakeECS) DescribeTaskDefinition(_ context.Context, in *ecs.DescribeTaskDefinitionInput, _ ...func(*ecs.Options)) (*ecs.DescribeTaskDefinitionOutput, error) {
	arn := aws.ToString(in.TaskDefinition)
	f.calls = append(f.calls, "DescribeTaskDefinition:"+arn)
	if f.describeRevisionErr != nil {
		return nil, f.describeRevisionErr
	}
	td, ok := f.families[arn]
	if !ok {
		return nil, fmt.Errorf("ClientException: Unable to describe task definition: %s", arn)
	}
	return &ecs.DescribeTaskDefinitionOutput{TaskDefinition: td}, nil
}

func (f *fakeECS) ListTasks(_ context.Context, in *ecs.ListTasksInput, _ ...func(*ecs.Options)) (*ecs.ListTasksOutput, error) {
	f.calls = append(f.calls, fmt.Sprintf("ListTasks:%s/%s", aws.ToString(in.ServiceName), in.DesiredStatus))
	if f.listTasksErr != nil {
		return nil, f.listTasksErr
	}
	var arns []string
	for _, task := range f.tasks[serviceKey(aws.ToString(in.Cluster), aws.ToString(in.ServiceName))] {
		arns = append(arns, aws.ToString(task.TaskArn))
	}
	return &ecs.ListTasksOutput{TaskArns: arns}, nil
}

func (f *fakeECS) DescribeTasks(_ context.Context, in *ecs.DescribeTasksInput, _ ...func(*ecs.Options)) (*ecs.DescribeTasksOutput, error) {
	f.calls = append(f.calls, fmt.Sprintf("DescribeTasks:%d", len(in.Tasks)))
	wanted := map[string]bool{}
	for _, arn := range in.Tasks {
		wanted[arn] = true
	}
	var out ecs.DescribeTasksOutput
	for _, tasks := range f.tasks {
		for _, task := range tasks {
			if wanted[aws.ToString(task.TaskArn)] {
				out.Tasks = append(out.Tasks, task)
			}
		}
	}
	return &out, nil
}

func ecsTags(tags map[string]string) []ecstypes.Tag {
	out := make([]ecstypes.Tag, 0, len(tags))
	for k, v := range tags {
		out = append(out, ecstypes.Tag{Key: aws.String(k), Value: aws.String(v)})
	}
	return out
}

// --- ELBv2 ---

// fakeELB is the read-only Elastic Load Balancing surface.
type fakeELB struct {
	groups  map[string]elbtypes.TargetGroup
	health  map[string][]elbtypes.TargetHealthDescription
	loadBal map[string]elbtypes.LoadBalancer

	calls []string

	describeGroupsErr error
	describeHealthErr error
}

func newFakeELB() *fakeELB {
	return &fakeELB{
		groups:  map[string]elbtypes.TargetGroup{},
		health:  map[string][]elbtypes.TargetHealthDescription{},
		loadBal: map[string]elbtypes.LoadBalancer{},
	}
}

// withHealthyIngress installs both data ports' target groups, each with two healthy targets, behind an
// active internal load balancer.
func (f *fakeELB) withHealthyIngress() *fakeELB {
	f.withLoadBalancer(lbActive)
	for _, port := range []int{4317, 4318} {
		f.withTargetGroup(port)
		f.withTargets(port, target("10.0.0.1", targetHealthy, "", ""), target("10.0.1.1", targetHealthy, "", ""))
	}
	return f
}

func (f *fakeELB) withLoadBalancer(state string) *fakeELB {
	f.loadBal[loadBalancerARN] = elbtypes.LoadBalancer{
		LoadBalancerArn: aws.String(loadBalancerARN),
		DNSName:         aws.String(testLBDNS),
		Scheme:          elbtypes.LoadBalancerSchemeEnumInternal,
		State:           &elbtypes.LoadBalancerState{Code: elbtypes.LoadBalancerStateEnum(state)},
	}
	return f
}

func (f *fakeELB) withTargetGroup(port int) *fakeELB {
	arn := targetGroupARN(port)
	f.groups[arn] = elbtypes.TargetGroup{
		TargetGroupArn:   aws.String(arn),
		TargetGroupName:  aws.String(deployer.TargetGroupName(testVPC, port)),
		Port:             aws.Int32(int32(port)),
		LoadBalancerArns: []string{loadBalancerARN},
	}
	return f
}

func (f *fakeELB) withTargets(port int, targets ...elbtypes.TargetHealthDescription) *fakeELB {
	f.health[targetGroupARN(port)] = targets
	return f
}

func target(id, state, reason, description string) elbtypes.TargetHealthDescription {
	return elbtypes.TargetHealthDescription{
		Target: &elbtypes.TargetDescription{Id: aws.String(id), Port: aws.Int32(4317)},
		TargetHealth: &elbtypes.TargetHealth{
			State:       elbtypes.TargetHealthStateEnum(state),
			Reason:      elbtypes.TargetHealthReasonEnum(reason),
			Description: aws.String(description),
		},
	}
}

func (f *fakeELB) DescribeTargetGroups(_ context.Context, in *elb.DescribeTargetGroupsInput, _ ...func(*elb.Options)) (*elb.DescribeTargetGroupsOutput, error) {
	f.calls = append(f.calls, fmt.Sprintf("DescribeTargetGroups:%d", len(in.TargetGroupArns)))
	if f.describeGroupsErr != nil {
		return nil, f.describeGroupsErr
	}
	var out elb.DescribeTargetGroupsOutput
	for _, arn := range in.TargetGroupArns {
		if group, ok := f.groups[arn]; ok {
			out.TargetGroups = append(out.TargetGroups, group)
		}
	}
	return &out, nil
}

func (f *fakeELB) DescribeTargetHealth(_ context.Context, in *elb.DescribeTargetHealthInput, _ ...func(*elb.Options)) (*elb.DescribeTargetHealthOutput, error) {
	arn := aws.ToString(in.TargetGroupArn)
	f.calls = append(f.calls, "DescribeTargetHealth:"+arn)
	if f.describeHealthErr != nil {
		return nil, f.describeHealthErr
	}
	return &elb.DescribeTargetHealthOutput{TargetHealthDescriptions: f.health[arn]}, nil
}

func (f *fakeELB) DescribeLoadBalancers(_ context.Context, in *elb.DescribeLoadBalancersInput, _ ...func(*elb.Options)) (*elb.DescribeLoadBalancersOutput, error) {
	f.calls = append(f.calls, fmt.Sprintf("DescribeLoadBalancers:%v", in.LoadBalancerArns))
	var out elb.DescribeLoadBalancersOutput
	for _, arn := range in.LoadBalancerArns {
		if lb, ok := f.loadBal[arn]; ok {
			out.LoadBalancers = append(out.LoadBalancers, lb)
		}
	}
	return &out, nil
}

// --- clients, store and planner ---

type fakeClients struct {
	ecs *fakeECS
	elb *fakeELB
	err error
}

func newFakeClients() *fakeClients { return &fakeClients{ecs: newFakeECS(), elb: newFakeELB()} }

func (f *fakeClients) ECS(context.Context, string) (ECSReader, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.ecs, nil
}

func (f *fakeClients) LoadBalancers(context.Context, string) (ELBReader, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.elb, nil
}

// fakeStore records every write in order, so a test can assert both what was written and — more often —
// that nothing was.
type fakeStore struct {
	record  basestore.CollectorRecord
	missing bool
	getErr  error

	writes []string

	infrastructure *basestore.CollectorRecord
	endpoint       []string
	image          []string
	scaling        scalingWrite
	status         collectorstatus.Status
	message        string
}

type scalingWrite struct {
	written                      bool
	mode                         string
	desired, running, minC, maxC *int
	cpu, memory                  string
}

func newFakeStore() *fakeStore { return &fakeStore{missing: true} }

// withRecord installs the row as it stands before this pass.
func (s *fakeStore) withRecord(mutate func(*basestore.CollectorRecord)) *fakeStore {
	rec := basestore.CollectorRecord{
		Key:       testKey(),
		NetworkID: testVPC,
		Mode:      "automatic",
		Origin:    "odigos",
		Status:    collectorstatus.StatusReady,
	}
	if mutate != nil {
		mutate(&rec)
	}
	s.record, s.missing = rec, false
	return s
}

func (s *fakeStore) GetManagedCollector(_ context.Context, key basestore.CollectorKey) (basestore.CollectorRecord, error) {
	if s.getErr != nil {
		return basestore.CollectorRecord{}, s.getErr
	}
	if s.missing {
		return basestore.CollectorRecord{}, fmt.Errorf("%w: %s", basestore.ErrCollectorNotFound, key.Region)
	}
	return s.record, nil
}

func (s *fakeStore) UpsertManagedCollector(_ context.Context, _ basestore.CollectorKey, networkID, kind, mode, origin string, _ *time.Time) error {
	s.writes = append(s.writes, fmt.Sprintf("Upsert:%s/%s/%s/%s", networkID, kind, mode, origin))
	return nil
}

func (s *fakeStore) RecordCollectorInfrastructure(_ context.Context, _ basestore.CollectorKey, rec basestore.CollectorRecord) error {
	s.writes = append(s.writes, "RecordInfrastructure")
	copied := rec
	s.infrastructure = &copied
	return nil
}

func (s *fakeStore) RecordCollectorEndpoint(_ context.Context, _ basestore.CollectorKey, grpc, http, source, tlsMode, tlsTermination, ingressTier string) error {
	s.writes = append(s.writes, "RecordEndpoint")
	s.endpoint = []string{grpc, http, source, tlsMode, tlsTermination, ingressTier}
	return nil
}

func (s *fakeStore) RecordCollectorImage(_ context.Context, _ basestore.CollectorKey, digest, tag, arch, licenseRef string, _ *time.Time) error {
	s.writes = append(s.writes, "RecordImage")
	s.image = []string{digest, tag, arch, licenseRef}
	return nil
}

func (s *fakeStore) RecordCollectorScaling(_ context.Context, _ basestore.CollectorKey, mode string, desired, running, minC, maxC *int, cpu, memory string) error {
	s.writes = append(s.writes, "RecordScaling")
	s.scaling = scalingWrite{true, mode, desired, running, minC, maxC, cpu, memory}
	return nil
}

func (s *fakeStore) SetCollectorStatus(_ context.Context, _ basestore.CollectorKey, status collectorstatus.Status, message string) error {
	s.writes = append(s.writes, "SetStatus:"+string(status))
	s.status, s.message = status, message
	return nil
}

// fakePlanner hands back a plan, or refuses to.
type fakePlanner struct {
	plan placement.Plan
	err  error
	// calls counts, because CollectorNetworks is what caches the plan for the pass.
	calls int
}

func (p *fakePlanner) CollectorPlan(context.Context) (placement.Plan, error) {
	p.calls++
	if p.err != nil {
		return placement.Plan{}, p.err
	}
	return p.plan, nil
}

// --- helpers ---

func testObserver(clients *fakeClients, store *fakeStore, mode string) *Observer {
	return &Observer{
		Identity:          testIdentity(),
		Kind:              "aws.fargate-collector",
		Mode:              mode,
		OpAMPEndpoint:     testEndpoint,
		Pin:               testPin(),
		HeartbeatInterval: 30 * time.Second,
		Settings:          func() collectorsettings.Settings { return testSettings() },
		Planner:           &fakePlanner{},
		Store:             store,
		Clients:           clients,
		Now:               func() time.Time { return testNow },
	}
}

// testSettings is the resolved tree for a network nobody has overridden.
func testSettings() collectorsettings.Settings {
	return collectorsettings.Settings{
		Enabled: true,
		Defaults: collectorsettings.Network{
			Enabled:    true,
			CPU:        collectorsettings.DefaultCPU,
			Memory:     collectorsettings.DefaultMemory,
			ClusterRef: deployer.ClusterName(),
			Scaling: collectorsettings.Scaling{
				Mode: collectorsettings.ScalingManual, DesiredCount: collectorsettings.DefaultDesiredCount,
			},
		},
	}
}

func codes(problems []collectorstatus.Problem) []string {
	out := make([]string, 0, len(problems))
	for _, p := range problems {
		out = append(out, p.Code)
	}
	return out
}

// faults are the problems above Info. Info is the standing-posture channel — the plaintext default rides
// there on every pass for as long as it applies — so a test that asserted "no problems" would be
// asserting that a correctly reported posture is a bug.
func faults(problems []collectorstatus.Problem) []collectorstatus.Problem {
	var out []collectorstatus.Problem
	for _, p := range problems {
		if p.Severity != collectorstatus.SeverityInfo {
			out = append(out, p)
		}
	}
	return out
}

func hasProblem(problems []collectorstatus.Problem, code string) bool {
	for _, p := range problems {
		if p.Code == code {
			return true
		}
	}
	return false
}

// problemWith returns the problem carrying a code, failing the test when there is none.
func problemWith(t *testing.T, problems []collectorstatus.Problem, code string) collectorstatus.Problem {
	t.Helper()
	for _, p := range problems {
		if p.Code == code {
			return p
		}
	}
	t.Fatalf("no %s problem in %v", code, codes(problems))
	return collectorstatus.Problem{}
}

func wrote(writes []string, prefix string) bool {
	for _, w := range writes {
		if strings.HasPrefix(w, prefix) {
			return true
		}
	}
	return false
}

var errThrottled = errors.New("ThrottlingException: Rate exceeded")
