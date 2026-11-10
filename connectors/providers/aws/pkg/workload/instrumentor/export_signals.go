package instrumentor

import (
	baseapi "github.com/odigos-io/odigos-enterprise/connectors/base/api"
	domaindest "github.com/odigos-io/odigos-enterprise/connectors/base/domain/destination"
)

func applySignalActivation(env map[string]string, dest domaindest.OTLPTarget, language string) {
	for key, value := range dest.LambdaSignalActivation(language) {
		env[key] = value
	}
}

func lambdaSignalRoutingNote(routing domaindest.Routing, runtime string) *baseapi.WorkloadRoutingNote {
	language := LanguageFromRuntime(runtime)
	if (routing.Target.Profiles || language == LangRuby && (routing.Target.Metrics || routing.Target.Logs)) && routing.Outcome != domaindest.RoutingError {
		note := routing.Target.LambdaCollectionSummary(language)
		if routing.Note != "" {
			note += " " + routing.Note
		}
		return &baseapi.WorkloadRoutingNote{Code: "collected-signals", Message: note, Transient: false}
	}
	return workloadRoutingNote(routing)
}

func fargateSignalRoutingNote(routing domaindest.Routing, targets []containerTarget) *baseapi.WorkloadRoutingNote {
	languages := make([]string, 0, len(targets))
	hasRuby := false
	for _, target := range targets {
		languages = append(languages, target.recipe.lang)
		hasRuby = hasRuby || target.recipe.lang == LangRuby
	}
	if (routing.Target.Profiles || hasRuby && (routing.Target.Metrics || routing.Target.Logs)) && routing.Outcome != domaindest.RoutingError {
		note := routing.Target.CollectionSummary(languages...)
		if routing.Note != "" {
			note += " " + routing.Note
		}
		return &baseapi.WorkloadRoutingNote{Code: "collected-signals", Message: note, Transient: false}
	}
	return workloadRoutingNote(routing)
}
