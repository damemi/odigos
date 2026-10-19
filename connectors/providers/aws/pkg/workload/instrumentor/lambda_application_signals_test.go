package instrumentor

import (
	"context"
	"errors"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/applicationsignals"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	iamtypes "github.com/aws/aws-sdk-go-v2/service/iam/types"
	domaindest "github.com/odigos-io/odigos-enterprise/connectors/base/domain/destination"
)

type applicationSignalsSetupFake struct {
	policyMissing, discoveryMissing bool
	attached                        bool
	attachCalls, discoveryCalls     int
	listCalls                       int
}

func (f *applicationSignalsSetupFake) GetPolicy(_ context.Context, in *iam.GetPolicyInput, _ ...func(*iam.Options)) (*iam.GetPolicyOutput, error) {
	if aws.ToString(in.PolicyArn) != "arn:aws:iam::aws:policy/"+lambdaApplicationSignalsPolicy {
		return nil, errors.New("unexpected policy ARN")
	}
	if f.policyMissing {
		return nil, errors.New("policy not found")
	}
	return &iam.GetPolicyOutput{}, nil
}
func (f *applicationSignalsSetupFake) ListAttachedRolePolicies(_ context.Context, in *iam.ListAttachedRolePoliciesInput, _ ...func(*iam.Options)) (*iam.ListAttachedRolePoliciesOutput, error) {
	f.listCalls++
	if aws.ToString(in.RoleName) != "checkout" {
		return nil, errors.New("role path was not removed")
	}
	if in.Marker == nil {
		return &iam.ListAttachedRolePoliciesOutput{IsTruncated: true, Marker: aws.String("next")}, nil
	}
	result := &iam.ListAttachedRolePoliciesOutput{}
	if f.attached {
		result.AttachedPolicies = []iamtypes.AttachedPolicy{{PolicyArn: aws.String("arn:aws:iam::aws:policy/" + lambdaApplicationSignalsPolicy)}}
	}
	return result, nil
}
func (f *applicationSignalsSetupFake) AttachRolePolicy(_ context.Context, _ *iam.AttachRolePolicyInput, _ ...func(*iam.Options)) (*iam.AttachRolePolicyOutput, error) {
	f.attachCalls++
	f.attached = true
	return &iam.AttachRolePolicyOutput{}, nil
}
func (f *applicationSignalsSetupFake) GetRole(_ context.Context, in *iam.GetRoleInput, _ ...func(*iam.Options)) (*iam.GetRoleOutput, error) {
	if aws.ToString(in.RoleName) != applicationSignalsDiscoveryRole {
		return nil, errors.New("unexpected discovery role")
	}
	if f.discoveryMissing {
		return nil, &iamtypes.NoSuchEntityException{Message: aws.String("missing")}
	}
	return &iam.GetRoleOutput{}, nil
}
func (f *applicationSignalsSetupFake) StartDiscovery(context.Context, *applicationsignals.StartDiscoveryInput, ...func(*applicationsignals.Options)) (*applicationsignals.StartDiscoveryOutput, error) {
	f.discoveryCalls++
	f.discoveryMissing = false
	return &applicationsignals.StartDiscoveryOutput{}, nil
}

func TestApplicationSignalsAutomaticSetupConverges(t *testing.T) {
	f := &applicationSignalsSetupFake{discoveryMissing: true}
	for n := 0; n < 2; n++ {
		if err := ensureLambdaApplicationSignals(context.Background(), f, f, "arn:aws:iam::123456789012:role/services/checkout"); err != nil {
			t.Fatal(err)
		}
	}
	if f.attachCalls != 1 || f.discoveryCalls != 1 || f.listCalls != 4 {
		t.Fatalf("setup did not converge: %+v", f)
	}
}

func TestApplicationSignalsMissingPolicyStopsSetup(t *testing.T) {
	f := &applicationSignalsSetupFake{policyMissing: true, discoveryMissing: true}
	if err := ensureLambdaApplicationSignals(context.Background(), f, f, "arn:aws:iam::123456789012:role/checkout"); err == nil {
		t.Fatal("missing existing policy was accepted")
	}
	if f.attachCalls != 0 || f.discoveryCalls != 0 {
		t.Fatal("mutated AWS after policy preflight failed")
	}
}

func TestApplicationSignalsRequiresTracesAndMetrics(t *testing.T) {
	for _, language := range []string{"java", "nodejs", "python"} {
		for mask := 1; mask < 8; mask++ {
			target := domaindest.OTLPTarget{Traces: mask&1 != 0, Metrics: mask&2 != 0, Logs: mask&4 != 0}
			err := validateLambdaApplicationSignalsTarget(language, language+"-lambda-adot-application-signals", target)
			if (err == nil) != (target.Traces && target.Metrics) {
				t.Errorf("%s target %+v: %v", language, target, err)
			}
			if err := validateLambdaApplicationSignalsTarget(language, language+"-lambda-adot", target); err != nil {
				t.Errorf("plain ADOT unnecessarily restricted: %v", err)
			}
		}
	}
}
