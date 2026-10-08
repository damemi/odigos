package collectorconfig

import (
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"

	"github.com/odigos-io/odigos/common"
	odigosconfig "github.com/odigos-io/odigos/common/config"
	odigosdest "github.com/odigos-io/odigos/destinations"

	"github.com/odigos-io/odigos-enterprise/connectors/base/domain/collectorstatus"
	"github.com/odigos-io/odigos-enterprise/connectors/base/domain/destination"
)

// unavailableTypes are catalog types whose configurer renders a component the managed Collector image
// does not ship (collector/builder-config.yaml). A document naming one is refused by the Collector as a
// whole, so these are neither offered nor accepted. TestEveryCatalogTypeRendersCompiledInComponents keeps
// this list honest in both directions.
var unavailableTypes = map[common.DestinationType]string{
	// The SAPM exporter was removed upstream; Splunk Observability is reached through splunkotlp.
	"splunk": "renders the sapm exporter, which the managed Collector image does not include",
}

// SupportsType reports whether a managed Collector can export to a destination type: it has an Odigos
// exporter configurer, and everything that configurer renders is compiled into the image.
func SupportsType(destType string) bool {
	t := common.DestinationType(destType)
	if _, excluded := unavailableTypes[t]; excluded {
		return false
	}
	configers, err := odigosconfig.LoadConfigers()
	if err != nil {
		return false
	}
	_, ok := configers[t]
	return ok
}

// renderedDestinations is what the Collector's destinations contribute to the document: the components
// their configurers declared, and the pipelines those configurers returned.
//
// The pipelines are the ENTRY pipelines — the ones Configer.ModifyConfig names in its result, which are
// the ones that must receive what the Collector receives. A configurer may declare others (fed from a
// connector it also declared); those arrive in `internal` and are rendered exactly as it wrote them.
type renderedDestinations struct {
	receivers  map[string]any
	exporters  map[string]any
	processors map[string]any
	extensions map[string]any
	connectors map[string]any
	// serviceExtensions are the extensions a configurer enabled (an auth extension, typically).
	serviceExtensions []string
	entry             map[string]odigosconfig.Pipeline
	internal          map[string]odigosconfig.Pipeline
}

// renderDestinations runs every destination through its Odigos exporter configurer — the same code a
// Kubernetes gateway and a VM agent render destinations with, so a type means the same thing here.
//
// A destination that cannot be rendered is left out with a warning rather than failing the document: the
// Collector's other destinations keep working, and the warning names the one that does not. Validation
// at write time dry-runs this same path (CheckRenderable), so this is the rare case of a stored row
// that stopped rendering after an upgrade.
func renderDestinations(dests []destination.Destination) (renderedDestinations, []collectorstatus.Problem) {
	out := renderedDestinations{
		receivers:  map[string]any{},
		exporters:  map[string]any{},
		processors: map[string]any{},
		extensions: map[string]any{},
		connectors: map[string]any{},
		entry:      map[string]odigosconfig.Pipeline{},
		internal:   map[string]odigosconfig.Pipeline{},
	}
	if len(dests) == 0 {
		return out, nil
	}

	configers, err := odigosconfig.LoadConfigers()
	if err != nil {
		return out, []collectorstatus.Problem{{
			Code:     "CollectorDestinationsUnrenderable",
			Severity: collectorstatus.SeverityError,
			Message:  fmt.Sprintf("could not load the destination configurers: %v", err),
		}}
	}
	if err := odigosdest.Load(); err != nil {
		return out, []collectorstatus.Problem{{
			Code:     "CollectorDestinationsUnrenderable",
			Severity: collectorstatus.SeverityError,
			Message:  fmt.Sprintf("could not load the destination catalog: %v", err),
		}}
	}

	// Name order, so the same set renders the same bytes whatever order the store returned it in.
	sorted := append([]destination.Destination(nil), dests...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Name < sorted[j].Name })

	var problems []collectorstatus.Problem
	for _, d := range sorted {
		scratch, entry, err := renderOne(configers, d)
		if err == nil {
			err = out.merge(scratch, entry)
		}
		if err != nil {
			problems = append(problems, collectorstatus.Problem{
				Code:     "CollectorDestinationUnrenderable",
				Severity: collectorstatus.SeverityDegraded,
				Message: fmt.Sprintf("destination %q (%s) is left out of the Collector configuration: %v",
					d.Name, d.Type, err),
			})
		}
	}
	return out, problems
}

// CheckRenderable dry-runs one destination through its configurer, so a destination the Collector could
// never be configured with is refused when it is written rather than dropped when it is rendered.
//
// Configurers have no separate validation: every one of them rejects bad field values only inside
// ModifyConfig, which is why this renders instead of inspecting.
func CheckRenderable(d destination.Destination) error {
	configers, err := odigosconfig.LoadConfigers()
	if err != nil {
		return fmt.Errorf("load destination configurers: %w", err)
	}
	if err := odigosdest.Load(); err != nil {
		return fmt.Errorf("load destination catalog: %w", err)
	}
	scratch, entry, err := renderOne(configers, d)
	if err != nil {
		return err
	}
	if len(entry) == 0 {
		return fmt.Errorf("destination type %q renders no pipeline for signals %s",
			d.Type, strings.Join(d.Signals, ", "))
	}
	return (&renderedDestinations{
		receivers: map[string]any{}, exporters: map[string]any{}, processors: map[string]any{},
		extensions: map[string]any{}, connectors: map[string]any{},
		entry: map[string]odigosconfig.Pipeline{}, internal: map[string]odigosconfig.Pipeline{},
	}).merge(scratch, entry)
}

// renderOne runs one destination's configurer against an empty configuration, so everything in the
// result belongs to that destination alone — which is what lets its secrets be substituted without
// reaching another destination's components.
func renderOne(configers map[common.DestinationType]odigosconfig.Configer, d destination.Destination) (
	*odigosconfig.Config, []string, error) {
	if reason, excluded := unavailableTypes[common.DestinationType(d.Type)]; excluded {
		return nil, nil, fmt.Errorf("destination type %q %s", d.Type, reason)
	}
	configer, ok := configers[common.DestinationType(d.Type)]
	if !ok {
		return nil, nil, fmt.Errorf("no exporter configurer for destination type %q", d.Type)
	}
	spec, ok := odigosdest.GetDestinationByType(d.Type)
	if !ok {
		return nil, nil, fmt.Errorf("destination type %q is not in the Odigos destination catalog", d.Type)
	}
	secretFields := map[string]bool{}
	for _, f := range spec.Spec.Fields {
		if f.Secret {
			secretFields[f.Name] = true
		}
	}

	// Non-secret values go to the configurer; secret ones never do. The configurers reference a secret as
	// ${FIELD_NAME} — in Kubernetes an environment variable mounted from the destination's Secret — and
	// the managed Collector has no such variables, so the reference is resolved below instead.
	values := map[string]string{}
	secrets := map[string]string{}
	for k, v := range d.Config {
		s := configString(v)
		if secretFields[k] {
			secrets[k] = s
			continue
		}
		values[k] = escapeExpansion(s)
	}

	scratch := &odigosconfig.Config{
		Receivers:  odigosconfig.GenericMap{},
		Exporters:  odigosconfig.GenericMap{},
		Processors: odigosconfig.GenericMap{},
		Extensions: odigosconfig.GenericMap{},
		Connectors: odigosconfig.GenericMap{},
		Service:    odigosconfig.Service{Pipelines: map[string]odigosconfig.Pipeline{}},
	}
	entry, err := configer.ModifyConfig(&exporterConfigurer{
		id: d.Name, destType: common.DestinationType(d.Type), config: values, signals: collectorSignals(d.Signals),
	}, scratch)
	if err != nil {
		return nil, nil, err
	}

	// Every secret field of the type is resolved, set or not: an unset optional secret becomes empty
	// rather than an environment lookup on the Collector's task, whose environment holds the OpAMP
	// credential.
	replacements := make(map[string]string, len(secretFields))
	for field := range secretFields {
		replacements["${"+field+"}"] = escapeExpansion(secrets[field])
	}
	for _, section := range []*odigosconfig.GenericMap{
		&scratch.Receivers, &scratch.Exporters, &scratch.Processors, &scratch.Extensions, &scratch.Connectors,
	} {
		resolved, err := substitute(*section, replacements)
		if err != nil {
			return nil, nil, err
		}
		*section = resolved
	}
	return scratch, entry, nil
}

// merge adds one destination's rendering to the set. A component name another destination already
// declared differently is refused: configurers name components after the destination, so a clash is a
// configurer that did not, and two destinations silently sharing one exporter would send one's
// telemetry with the other's credentials.
func (r *renderedDestinations) merge(scratch *odigosconfig.Config, entry []string) error {
	for _, s := range []struct {
		name     string
		from, to map[string]any
	}{
		{"receiver", scratch.Receivers, r.receivers},
		{"exporter", scratch.Exporters, r.exporters},
		{"processor", scratch.Processors, r.processors},
		{"extension", scratch.Extensions, r.extensions},
		{"connector", scratch.Connectors, r.connectors},
	} {
		for k, v := range s.from {
			if existing, ok := s.to[k]; ok && !reflect.DeepEqual(existing, v) {
				return fmt.Errorf("%s %q is already declared by another destination", s.name, k)
			}
		}
	}
	for name := range scratch.Service.Pipelines {
		if _, ok := r.entry[name]; ok {
			return fmt.Errorf("pipeline %q is already declared by another destination", name)
		}
		if _, ok := r.internal[name]; ok {
			return fmt.Errorf("pipeline %q is already declared by another destination", name)
		}
	}

	for _, s := range []struct{ from, to map[string]any }{
		{scratch.Receivers, r.receivers}, {scratch.Exporters, r.exporters}, {scratch.Processors, r.processors},
		{scratch.Extensions, r.extensions}, {scratch.Connectors, r.connectors},
	} {
		for k, v := range s.from {
			s.to[k] = v
		}
	}
	isEntry := map[string]bool{}
	for _, name := range entry {
		isEntry[name] = true
	}
	for name, p := range scratch.Service.Pipelines {
		if isEntry[name] {
			r.entry[name] = p
		} else {
			r.internal[name] = p
		}
	}
	for _, ext := range scratch.Service.Extensions {
		if !containsString(r.serviceExtensions, ext) {
			r.serviceExtensions = append(r.serviceExtensions, ext)
		}
	}
	return nil
}

// substitute replaces secret references in every string of a section, after normalizing the section to
// plain maps and slices. Normalizing through JSON is what makes the walk total: configurers build their
// output from several named map types (GenericMap, map[string]interface{}, struct{} placeholders), and a
// walk that missed one would leave a reference unresolved.
func substitute(section odigosconfig.GenericMap, replacements map[string]string) (odigosconfig.GenericMap, error) {
	if len(section) == 0 {
		return section, nil
	}
	raw, err := json.Marshal(section)
	if err != nil {
		return nil, fmt.Errorf("normalizing rendered components: %w", err)
	}
	var plain map[string]any
	if err := json.Unmarshal(raw, &plain); err != nil {
		return nil, fmt.Errorf("normalizing rendered components: %w", err)
	}
	return odigosconfig.GenericMap(replaceInValue(plain, replacements).(map[string]any)), nil
}

func replaceInValue(v any, replacements map[string]string) any {
	switch t := v.(type) {
	case string:
		for ref, value := range replacements {
			t = strings.ReplaceAll(t, ref, value)
		}
		return t
	case map[string]any:
		for k, item := range t {
			t[k] = replaceInValue(item, replacements)
		}
		return t
	case []any:
		for i, item := range t {
			t[i] = replaceInValue(item, replacements)
		}
		return t
	default:
		return v
	}
}

// escapeExpansion stops the Collector expanding a value it was given. Its configuration loader expands
// every ${...} it finds, so an unescaped value — secret or not — could name an environment variable of
// the Collector's task and have the Collector send it to the destination. $$ is the loader's literal $.
func escapeExpansion(s string) string { return strings.ReplaceAll(s, "$", "$$") }

// configString renders a stored config value the way the configurers expect it: they take strings, and
// the API accepts JSON scalars.
func configString(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	default:
		return fmt.Sprintf("%v", t)
	}
}

// collectorSignals keeps the signals a managed Collector exports. Validation already refuses the rest;
// this keeps a stored row from an older rule set from declaring a pipeline nothing feeds.
func collectorSignals(in []string) []common.ObservabilitySignal {
	var out []common.ObservabilitySignal
	for _, s := range in {
		switch common.ObservabilitySignal(s) {
		case common.TracesObservabilitySignal, common.MetricsObservabilitySignal, common.LogsObservabilitySignal:
			out = append(out, common.ObservabilitySignal(s))
		}
	}
	return out
}

func containsString(list []string, s string) bool {
	for _, item := range list {
		if item == s {
			return true
		}
	}
	return false
}

// exporterConfigurer is one destination as a configurer reads it.
type exporterConfigurer struct {
	id       string
	destType common.DestinationType
	config   map[string]string
	signals  []common.ObservabilitySignal
}

func (e *exporterConfigurer) GetID() string                            { return e.id }
func (e *exporterConfigurer) GetType() common.DestinationType          { return e.destType }
func (e *exporterConfigurer) GetConfig() map[string]string             { return e.config }
func (e *exporterConfigurer) GetSignals() []common.ObservabilitySignal { return e.signals }
