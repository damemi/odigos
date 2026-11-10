package awsprovider

import (
	"context"
	"fmt"

	baseapi "github.com/odigos-io/odigos/connectors/base/api"
	"github.com/odigos-io/odigos/connectors/base/domain/source"
	"github.com/odigos-io/odigos/connectors/providers/aws/pkg/agentcore"
	ins "github.com/odigos-io/odigos/connectors/providers/aws/pkg/workload/instrumentor"
)

func (p *Provider) RequiresGlobalOTLPDestination(kind string) bool { return kind == agentcore.Kind }

func (p *Provider) DescribeInstrumentation(ctx context.Context, w *baseapi.DiscoveredWorkload) {
	language := w.RuntimeLanguage
	if w.Kind != "aws.lambda" && w.Overrides.RuntimeLanguage != "" {
		language = w.Overrides.RuntimeLanguage
	}
	w.AvailableDistros = []baseapi.InstrumentationDistro{}
	if w.Kind == "aws.bedrock-agentcore-runtime" {
		w.AvailableDistros = ins.AgentCoreDistros(language)
		if d, err := ins.ResolveAgentCoreDistro(language, w.Overrides.OtelDistroName); err == nil {
			w.EffectiveDistro = d.Name
			w.InstrumentationVersion = d.Version
		}
	} else if w.Kind == "aws.lambda" {
		activation := ins.ActivationHandler
		runtime := ""
		if fn, ok, _ := p.workloadStdb.GetLambdaFunction(ctx, w.ExternalID); ok {
			runtime = fn.Runtime
			language = ins.LanguageFromRuntime(runtime)
			if ins.UsesLambdaWebAdapter(fn.Layers) {
				activation = ins.ActivationWebServer
			}
		}
		for _, d := range ins.LambdaDistros(language, activation) {
			if runtime == "" || ins.ValidateLambdaDistroRuntime(runtime, activation, d.Name) == nil {
				w.AvailableDistros = append(w.AvailableDistros, d)
			}
		}
		if d, err := ins.ResolveLambdaDistro(language, activation, w.Overrides.OtelDistroName); err == nil && (runtime == "" || ins.ValidateLambdaDistroRuntime(runtime, activation, d.Name) == nil) {
			w.EffectiveDistro, w.InstrumentationVersion = d.Name, d.Version
		}
	} else if w.Kind == "aws.fargate-task" {
		w.AvailableDistros = ins.FargateDistros()
		if w.Details != nil {
			for i := range w.Details.Containers {
				c := &w.Details.Containers[i]
				for _, d := range w.AvailableDistros {
					if c.Instrumented && c.Distro == c.RuntimeLanguage && ins.FargateDistroMatchesLanguage(d.Name, c.RuntimeLanguage) {
						c.Distro = d.Name
					}
				}
			}
		}
		for _, d := range w.AvailableDistros {
			if ins.FargateDistroMatchesLanguage(d.Name, language) {
				w.EffectiveDistro, w.InstrumentationVersion = d.Name, d.Version
			}
		}
	}
}

func (p *Provider) ValidateSourceInstrumentation(ctx context.Context, src source.Source, w *baseapi.DiscoveredWorkload) error {
	if src.Kind == "aws.bedrock-agentcore-runtime" {
		if w == nil {
			return fmt.Errorf("cannot select instrumentation for an undiscovered workload")
		}
		language := src.Config.RuntimeOverride.Language
		if language == "" {
			runtimes, err := p.workloadStdb.ListAgentCore(ctx)
			if err != nil {
				return err
			}
			for _, r := range runtimes {
				if r.ARN == w.ExternalID {
					language = r.Language
					break
				}
			}
		}
		if language == "" && src.Config.DistroOverride.OtelDistroName == "" {
			return nil
		}
		_, err := ins.ResolveAgentCoreDistro(language, src.Config.DistroOverride.OtelDistroName)
		return err
	}
	if src.Kind != "aws.lambda" && src.Kind != "aws.fargate-task" {
		return nil
	}
	if w == nil {
		return fmt.Errorf("cannot select instrumentation for an undiscovered workload")
	}
	if src.Kind == "aws.lambda" {
		activation := ins.ActivationHandler
		// LookupWorkload returns identity only. Read the stored AWS runtime rather than relying on
		// fields populated by the workload-report listing path.
		language := w.RuntimeLanguage
		runtime := ""
		if fn, ok, err := p.workloadStdb.GetLambdaFunction(ctx, w.ExternalID); err != nil {
			return err
		} else if ok {
			language = ins.LanguageFromRuntime(fn.Runtime)
			runtime = fn.Runtime
			if ins.UsesLambdaWebAdapter(fn.Layers) {
				activation = ins.ActivationWebServer
			}
		}
		_, err := ins.ResolveLambdaDistro(language, activation, src.Config.DistroOverride.OtelDistroName)
		if runtime != "" {
			err = ins.ValidateLambdaDistroRuntime(runtime, activation, src.Config.DistroOverride.OtelDistroName)
		}
		if language == "" && src.Config.DistroOverride.OtelDistroName == "" {
			return nil
		}
		return err
	}
	hasChoice := src.Config.DistroOverride.OtelDistroName != ""
	for _, c := range src.Config.ContainerOverrides {
		hasChoice = hasChoice || c.DistroOverride.OtelDistroName != ""
	}
	if !hasChoice {
		return nil
	}
	if w.RuntimeLanguage == "" && w.Details == nil {
		inventory, err := p.workloadStdb.ListWorkloads(ctx)
		if err != nil {
			return err
		}
		for _, observed := range inventory {
			if observed.Kind == w.Kind && observed.ExternalID == w.ExternalID {
				w = &observed
				break
			}
		}
	}
	allowed := ins.FargateDistros()
	check := func(override source.DistroOverride, language string) error {
		if override.OtelDistroName == "" {
			return nil
		}
		for _, d := range allowed {
			if d.Name == override.OtelDistroName && (language == "" || ins.FargateDistroMatchesLanguage(d.Name, language)) {
				return nil
			}
		}
		return fmt.Errorf("unsupported instrumentation distro %q for %s (%s)", override.OtelDistroName, src.Kind, language)
	}
	language := src.Config.RuntimeOverride.Language
	if language == "" {
		language = w.RuntimeLanguage
	}
	if err := check(src.Config.DistroOverride, language); err != nil {
		return err
	}
	for _, c := range src.Config.ContainerOverrides {
		lang := c.RuntimeOverride.Language
		if lang == "" && w.Details != nil {
			for _, observed := range w.Details.Containers {
				if observed.Name == c.ContainerName {
					lang = observed.RuntimeLanguage
					break
				}
			}
		}
		if lang == "" {
			lang = language
		}
		if err := check(c.DistroOverride, lang); err != nil {
			return err
		}
	}
	return nil
}
