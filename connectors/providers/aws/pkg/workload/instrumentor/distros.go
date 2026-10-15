package instrumentor

import (
	_ "embed"
	"encoding/json"
	"fmt"

	baseapi "github.com/odigos-io/odigos-enterprise/connectors/base/api"
	"github.com/odigos-io/odigos-enterprise/connectors/base/domain/plan"
)

const JavaAgentDistro = "java-lambda-otel-agent"
const JavaWrapperDistro = "java-lambda-otel-wrapper"

//go:embed data/lambda_distros.json
var lambdaDistroJSON []byte

type lambdaRecipe struct {
	baseapi.InstrumentationDistro
	ExecWrapper string `json:"execWrapper"`
}

func lambdaRecipes() []lambdaRecipe {
	var all []lambdaRecipe
	if err := json.Unmarshal(lambdaDistroJSON, &all); err != nil {
		panic(err)
	}
	return all
}

// LambdaDistros advertises one concrete activation recipe per supported SDK selection.
func LambdaDistros(language, activation string) []baseapi.InstrumentationDistro {
	out := []baseapi.InstrumentationDistro{}
	for _, d := range lambdaRecipes() {
		if d.Language != language {
			continue
		}
		if activation == ActivationWebServer && (language == "ruby" || (language == "java" && d.Name != JavaAgentDistro)) {
			continue
		}
		out = append(out, d.InstrumentationDistro)
	}
	return out
}

func ResolveLambdaDistro(language, activation, name string) (baseapi.InstrumentationDistro, error) {
	// Former script-name overrides retain their meaning, but are not advertised as SDKs.
	if language == "java" {
		for _, d := range lambdaRecipes() {
			if "/opt/"+name == d.ExecWrapper {
				name = d.Name
				break
			}
		}
	}
	for _, d := range LambdaDistros(language, activation) {
		if name == d.Name || (name == "" && d.IsDefault) {
			return d, nil
		}
	}
	return baseapi.InstrumentationDistro{}, fmt.Errorf("unsupported instrumentation distro %q for Lambda %s (%s)", name, language, activation)
}

func DesiredLambdaPlanForDistro(lang, activation, name string) (plan.Plan, error) {
	d, err := ResolveLambdaDistro(lang, activation, name)
	if err != nil {
		return plan.Plan{}, err
	}
	p, err := DesiredLambdaPlan(lang, activation)
	if err != nil {
		return p, err
	}
	if activation == ActivationHandler && lang == "java" {
		for i := range p.Fragments {
			for j := range p.Fragments[i].Env {
				e := &p.Fragments[i].Env[j]
				if e.Name == "AWS_LAMBDA_EXEC_WRAPPER" {
					e.Value = javaHandlerWrapperFor(d.Name)
				}
			}
		}
	}
	return p, p.Validate()
}

//go:embed data/fargate_distros.json
var fargateDistroJSON []byte

// FargateDistros advertises only recipes staged by the connector, never the global K8s/eBPF list.
func FargateDistros() []baseapi.InstrumentationDistro {
	var all []baseapi.InstrumentationDistro
	if err := json.Unmarshal(fargateDistroJSON, &all); err != nil {
		panic(err)
	}
	return all
}

// FargateDistroMatchesLanguage accepts the same runtime aliases as the injection recipes.
func FargateDistroMatchesLanguage(name, language string) bool {
	lang := normalizeLanguage(language)
	for _, d := range FargateDistros() {
		if d.Name == name && d.Language == lang {
			return true
		}
	}
	return false
}
