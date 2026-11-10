package instrumentor

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/lambda"
)

type lambdaAliasReader interface {
	ListAliases(context.Context, *lambda.ListAliasesInput, ...func(*lambda.Options)) (*lambda.ListAliasesOutput, error)
	GetFunctionConfiguration(context.Context, *lambda.GetFunctionConfigurationInput, ...func(*lambda.Options)) (*lambda.GetFunctionConfigurationOutput, error)
}

func aliasInstrumentationFingerprint(env map[string]string, layers []string) string {
	selected := map[string]string{EnvLambdaConfig: env[EnvLambdaConfig]}
	for _, key := range odigosEnvKeys {
		selected[key] = env[key]
	}
	if parsed, err := parseLambdaConfig(env[EnvLambdaConfig]); err == nil {
		encoded, _ := json.Marshal(parsed)
		selected[EnvLambdaConfig] = string(encoded)
	}
	encoded, _ := json.Marshal(selected)
	return string(encoded) + lambdaAppliedFingerprint(map[string]string{EnvLambdaConfig: selected[EnvLambdaConfig], "AWS_LAMBDA_EXEC_WRAPPER": env["AWS_LAMBDA_EXEC_WRAPPER"]}, layers)
}

// Aliases invoke immutable versions. Checking their primary and weighted versions
// prevents a conformant $LATEST from claiming coverage of uninstrumented production traffic.
func validateLambdaAliasCoverage(ctx context.Context, client lambdaAliasReader, arn string, env map[string]string, layers []string) error {
	want := aliasInstrumentationFingerprint(env, layers)
	checked := map[string]bool{}
	var marker *string
	for {
		page, err := client.ListAliases(ctx, &lambda.ListAliasesInput{FunctionName: aws.String(arn), Marker: marker})
		if err != nil {
			return fmt.Errorf("verify Lambda alias coverage (lambda:ListAliases): %w", err)
		}
		for _, alias := range page.Aliases {
			versions := []string{aws.ToString(alias.FunctionVersion)}
			if alias.RoutingConfig != nil {
				for version := range alias.RoutingConfig.AdditionalVersionWeights {
					versions = append(versions, version)
				}
			}
			for _, version := range versions {
				if checked[version] {
					continue
				}
				checked[version] = true
				live, err := client.GetFunctionConfiguration(ctx, &lambda.GetFunctionConfigurationInput{FunctionName: aws.String(arn), Qualifier: aws.String(version)})
				if err != nil {
					return fmt.Errorf("verify Lambda alias %s version %s: %w", aws.ToString(alias.Name), version, err)
				}
				if aliasInstrumentationFingerprint(currentEnv(live.Environment), layerARNs(live.Layers)) != want {
					return fmt.Errorf("Lambda $LATEST is configured, but alias %s invokes version %s with different instrumentation. Publish the instrumented configuration and update the alias through your deployment pipeline; Odigos does not move production aliases", aws.ToString(alias.Name), version)
				}
			}
		}
		if aws.ToString(page.NextMarker) == "" {
			return nil
		}
		if aws.ToString(page.NextMarker) == aws.ToString(marker) {
			return fmt.Errorf("Lambda returned a repeated alias pagination marker")
		}
		marker = page.NextMarker
	}
}
