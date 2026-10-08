package deployer

import (
	"context"
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"
	logstypes "github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs/types"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"
	smtypes "github.com/aws/aws-sdk-go-v2/service/secretsmanager/types"

	baseapi "github.com/odigos-io/odigos-enterprise/connectors/base/api"
	"github.com/odigos-io/odigos-enterprise/connectors/base/domain/collectorcredential"
	"github.com/odigos-io/odigos-enterprise/connectors/base/domain/collectorownership"
	basestore "github.com/odigos-io/odigos-enterprise/connectors/base/store"
	"github.com/odigos-io/odigos-enterprise/connectors/spec"
)

const (
	testRegion    = "eu-west-1"
	testAccount   = "123456789012"
	testConnector = "prod"
	testNetwork   = "vpc-0abc"
	testKind      = "aws.fargate-collector"
	testUID       = "uid-1"
)

func testIdentity() collectorownership.Identity {
	return collectorownership.Identity{ConnectorName: testConnector, ConnectorID: testUID}
}

func testKey() basestore.CollectorKey {
	return basestore.CollectorKey{Region: testRegion}
}

// ourTags is the tag set this connector's own objects carry.
func ourTags(network string) map[string]string {
	region := testRegion
	if network == "" {
		region = ""
	}
	return baseapi.CollectorManagedTags(testConnector, testUID, region, network)
}

// --- CloudWatch Logs ---

type fakeLogs struct {
	groups        map[string]map[string]string // name -> tags
	retention     map[string]int32
	calls         []string
	retentionErr  error
	tagResourceEr error
}

func newFakeLogs() *fakeLogs {
	return &fakeLogs{groups: map[string]map[string]string{}, retention: map[string]int32{}}
}

func (f *fakeLogs) CreateLogGroup(_ context.Context, in *cloudwatchlogs.CreateLogGroupInput, _ ...func(*cloudwatchlogs.Options)) (*cloudwatchlogs.CreateLogGroupOutput, error) {
	name := aws.ToString(in.LogGroupName)
	f.calls = append(f.calls, "CreateLogGroup:"+name)
	if _, exists := f.groups[name]; exists {
		return nil, &logstypes.ResourceAlreadyExistsException{}
	}
	f.groups[name] = in.Tags
	return &cloudwatchlogs.CreateLogGroupOutput{}, nil
}

func (f *fakeLogs) PutRetentionPolicy(_ context.Context, in *cloudwatchlogs.PutRetentionPolicyInput, _ ...func(*cloudwatchlogs.Options)) (*cloudwatchlogs.PutRetentionPolicyOutput, error) {
	f.calls = append(f.calls, "PutRetentionPolicy:"+aws.ToString(in.LogGroupName))
	if f.retentionErr != nil {
		return nil, f.retentionErr
	}
	f.retention[aws.ToString(in.LogGroupName)] = aws.ToInt32(in.RetentionInDays)
	return &cloudwatchlogs.PutRetentionPolicyOutput{}, nil
}

func (f *fakeLogs) TagResource(_ context.Context, in *cloudwatchlogs.TagResourceInput, _ ...func(*cloudwatchlogs.Options)) (*cloudwatchlogs.TagResourceOutput, error) {
	f.calls = append(f.calls, "TagResource:"+aws.ToString(in.ResourceArn))
	if f.tagResourceEr != nil {
		return nil, f.tagResourceEr
	}
	return &cloudwatchlogs.TagResourceOutput{}, nil
}

// --- Secrets Manager ---

type fakeSecret struct {
	arn         string
	tags        map[string]string
	versions    map[string][]string
	deletedDate *time.Time
	// values records what was written, so a test can prove the credential actually reached the store —
	// and, in the secrecy test, that nothing else did.
	values []string
}

type fakeSecrets struct {
	secrets   map[string]*fakeSecret
	calls     []string
	createErr error
	putErr    error
}

func newFakeSecrets() *fakeSecrets {
	return &fakeSecrets{secrets: map[string]*fakeSecret{}}
}

func (f *fakeSecrets) withSecret(name string, tags map[string]string, hasValue bool) *fakeSecrets {
	s := &fakeSecret{
		arn:      "arn:aws:secretsmanager:" + testRegion + ":" + testAccount + ":secret:" + name + "-AbCdEf",
		tags:     tags,
		versions: map[string][]string{},
	}
	if hasValue {
		s.versions["v1"] = []string{"AWSCURRENT"}
	}
	f.secrets[name] = s
	return f
}

// find resolves a SecretId, which may be a name or an ARN — the real API accepts both, and this package
// describes by name and then writes by ARN.
func (f *fakeSecrets) find(id string) (string, *fakeSecret) {
	if s, ok := f.secrets[id]; ok {
		return id, s
	}
	for name, s := range f.secrets {
		if s.arn == id {
			return name, s
		}
	}
	return "", nil
}

func (f *fakeSecrets) DescribeSecret(_ context.Context, in *secretsmanager.DescribeSecretInput, _ ...func(*secretsmanager.Options)) (*secretsmanager.DescribeSecretOutput, error) {
	id := aws.ToString(in.SecretId)
	f.calls = append(f.calls, "DescribeSecret:"+id)
	_, s := f.find(id)
	if s == nil {
		return nil, &smtypes.ResourceNotFoundException{}
	}
	return &secretsmanager.DescribeSecretOutput{
		ARN:                aws.String(s.arn),
		Tags:               secretTags(s.tags),
		VersionIdsToStages: s.versions,
		DeletedDate:        s.deletedDate,
	}, nil
}

func (f *fakeSecrets) CreateSecret(_ context.Context, in *secretsmanager.CreateSecretInput, _ ...func(*secretsmanager.Options)) (*secretsmanager.CreateSecretOutput, error) {
	name := aws.ToString(in.Name)
	f.calls = append(f.calls, "CreateSecret:"+name)
	if in.SecretString != nil {
		// Recorded as a call the secrecy test can see. EnsureSecret must never carry a value.
		f.calls = append(f.calls, "CreateSecretWithValue:"+name)
	}
	if f.createErr != nil {
		return nil, f.createErr
	}
	tags := map[string]string{}
	for _, t := range in.Tags {
		tags[aws.ToString(t.Key)] = aws.ToString(t.Value)
	}
	s := &fakeSecret{
		arn:      "arn:aws:secretsmanager:" + testRegion + ":" + testAccount + ":secret:" + name + "-AbCdEf",
		tags:     tags,
		versions: map[string][]string{},
	}
	f.secrets[name] = s
	return &secretsmanager.CreateSecretOutput{ARN: aws.String(s.arn), Name: in.Name}, nil
}

func (f *fakeSecrets) PutSecretValue(_ context.Context, in *secretsmanager.PutSecretValueInput, _ ...func(*secretsmanager.Options)) (*secretsmanager.PutSecretValueOutput, error) {
	id := aws.ToString(in.SecretId)
	f.calls = append(f.calls, "PutSecretValue:"+id)
	if f.putErr != nil {
		return nil, f.putErr
	}
	_, s := f.find(id)
	if s == nil {
		return nil, &smtypes.ResourceNotFoundException{}
	}
	version := fmt.Sprintf("v%d", len(s.values)+1)
	s.values = append(s.values, aws.ToString(in.SecretString))
	s.versions[version] = []string{"AWSCURRENT"}
	return &secretsmanager.PutSecretValueOutput{VersionId: aws.String(version)}, nil
}

func (f *fakeSecrets) TagResource(_ context.Context, in *secretsmanager.TagResourceInput, _ ...func(*secretsmanager.Options)) (*secretsmanager.TagResourceOutput, error) {
	id := aws.ToString(in.SecretId)
	f.calls = append(f.calls, "TagResource:"+id)
	_, s := f.find(id)
	if s == nil {
		return nil, &smtypes.ResourceNotFoundException{}
	}
	if s.tags == nil {
		s.tags = map[string]string{}
	}
	for _, t := range in.Tags {
		s.tags[aws.ToString(t.Key)] = aws.ToString(t.Value)
	}
	return &secretsmanager.TagResourceOutput{}, nil
}

func (f *fakeSecrets) RestoreSecret(_ context.Context, in *secretsmanager.RestoreSecretInput, _ ...func(*secretsmanager.Options)) (*secretsmanager.RestoreSecretOutput, error) {
	id := aws.ToString(in.SecretId)
	f.calls = append(f.calls, "RestoreSecret:"+id)
	if _, s := f.find(id); s != nil {
		s.deletedDate = nil
	}
	return &secretsmanager.RestoreSecretOutput{}, nil
}

// --- credential store ---

type fakeStore struct {
	rows        map[basestore.CollectorKey]*basestore.CollectorRecord
	credentials map[string]*collectorcredential.Record
	calls       []string
	issueErr    error
	nextID      int
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		rows:        map[basestore.CollectorKey]*basestore.CollectorRecord{},
		credentials: map[string]*collectorcredential.Record{},
	}
}

func (f *fakeStore) GetManagedCollector(_ context.Context, key basestore.CollectorKey) (basestore.CollectorRecord, error) {
	row, ok := f.rows[key]
	if !ok {
		return basestore.CollectorRecord{}, basestore.ErrCollectorNotFound
	}
	return *row, nil
}

func (f *fakeStore) UpsertManagedCollector(_ context.Context, key basestore.CollectorKey, networkID, kind, mode, origin string, _ *time.Time) error {
	f.calls = append(f.calls, "UpsertManagedCollector:"+key.Region+"/"+networkID+"/"+origin)
	f.rows[key] = &basestore.CollectorRecord{Key: key, NetworkID: networkID, Kind: kind, Mode: mode, Origin: origin}
	return nil
}

func (f *fakeStore) CollectorCredential(_ context.Context, collectorID, credentialID string) (collectorcredential.Record, error) {
	rec, ok := f.credentials[collectorID+"/"+credentialID]
	if !ok {
		return collectorcredential.Record{}, fmt.Errorf("%w: %s/%s", basestore.ErrCredentialNotFound, collectorID, credentialID)
	}
	return *rec, nil
}

func (f *fakeStore) IssueCollectorCredential(_ context.Context, req collectorcredential.IssueRequest) (collectorcredential.Issued, error) {
	f.calls = append(f.calls, "IssueCollectorCredential:"+req.Binding.NetworkID)
	if f.issueErr != nil {
		return collectorcredential.Issued{}, f.issueErr
	}
	minted, err := collectorcredential.Mint()
	if err != nil {
		return collectorcredential.Issued{}, err
	}
	f.nextID++
	collectorID := req.Binding.CollectorID
	if collectorID == "" {
		collectorID = fmt.Sprintf("col_%d", f.nextID)
	}
	credentialID := fmt.Sprintf("cred_%d", f.nextID)
	record := collectorcredential.NewRecord(req, collectorID, credentialID, minted.Hash, time.Now())
	f.credentials[collectorID+"/"+credentialID] = &record
	return collectorcredential.Issued{Minted: minted, Record: record}, nil
}

func (f *fakeStore) MarkCollectorCredentialActive(_ context.Context, collectorID, credentialID, secretVersion string) error {
	f.calls = append(f.calls, "MarkCollectorCredentialActive:"+credentialID+"@"+secretVersion)
	rec, ok := f.credentials[collectorID+"/"+credentialID]
	if !ok {
		return fmt.Errorf("no credential %s/%s", collectorID, credentialID)
	}
	rec.State = collectorcredential.StateActive
	rec.SecretVersion = secretVersion
	return nil
}

func (f *fakeStore) RecordCollectorIdentity(_ context.Context, key basestore.CollectorKey, collectorID, credentialRef, activeCredentialID string) error {
	f.calls = append(f.calls, "RecordCollectorIdentity:"+key.Region+"/"+collectorID)
	row, ok := f.rows[key]
	if !ok {
		return basestore.ErrCollectorNotFound
	}
	row.CollectorID, row.CredentialRef, row.ActiveCredentialID = collectorID, credentialRef, activeCredentialID
	return nil
}

// withLiveCredential seeds a Collector that already has a working credential — the steady state, and
// the one EnsureCredential must not disturb.
func (f *fakeStore) withLiveCredential(key basestore.CollectorKey, state collectorcredential.State) *fakeStore {
	const collectorID, credentialID = "col_existing", "cred_existing"
	f.rows[key] = &basestore.CollectorRecord{
		Key: key, Kind: testKind, Mode: spec.ModeAutomatic, Origin: string(collectorownership.OriginOdigos),
		CollectorID:        collectorID,
		CredentialRef:      "arn:aws:secretsmanager:" + testRegion + ":" + testAccount + ":secret:x-AbCdEf",
		ActiveCredentialID: credentialID,
	}
	f.credentials[collectorID+"/"+credentialID] = &collectorcredential.Record{
		CredentialID: credentialID,
		Binding:      collectorcredential.Binding{CollectorID: collectorID},
		State:        state,
	}
	return f
}

func (f *fakeStore) called(prefix string) bool {
	for _, c := range f.calls {
		if len(c) >= len(prefix) && c[:len(prefix)] == prefix {
			return true
		}
	}
	return false
}
