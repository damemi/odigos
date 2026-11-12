package instrumentor

import (
	"context"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/lambda"
	lambdatypes "github.com/aws/aws-sdk-go-v2/service/lambda/types"
)

type aliasReaderFake struct {
	env      map[string]string
	weighted bool
}

func (f aliasReaderFake) ListAliases(context.Context, *lambda.ListAliasesInput, ...func(*lambda.Options)) (*lambda.ListAliasesOutput, error) {
	a := lambdatypes.AliasConfiguration{Name: aws.String("production"), FunctionVersion: aws.String("1")}
	if f.weighted {
		a.RoutingConfig = &lambdatypes.AliasRoutingConfiguration{AdditionalVersionWeights: map[string]float64{"2": 0.1}}
	}
	return &lambda.ListAliasesOutput{Aliases: []lambdatypes.AliasConfiguration{a}}, nil
}
func (f aliasReaderFake) GetFunctionConfiguration(_ context.Context, in *lambda.GetFunctionConfigurationInput, _ ...func(*lambda.Options)) (*lambda.GetFunctionConfigurationOutput, error) {
	env := f.env
	if aws.ToString(in.Qualifier) == "2" {
		env = map[string]string{}
	}
	return &lambda.GetFunctionConfigurationOutput{Environment: &lambdatypes.EnvironmentResponse{Variables: env}}, nil
}
func TestAliasCoverageChecksWeightedVersions(t *testing.T) {
	env := map[string]string{EnvLambdaConfig: `{"version":2,"service":"app","signals":["traces"],"endpoint":"sink:4317"}`}
	if err := validateLambdaAliasCoverage(context.Background(), aliasReaderFake{env: env}, "function", env, nil); err != nil {
		t.Fatal(err)
	}
	if err := validateLambdaAliasCoverage(context.Background(), aliasReaderFake{env: env, weighted: true}, "function", env, nil); err == nil {
		t.Fatal("unconfigured canary version was reported covered")
	}
}
