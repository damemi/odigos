package odigosconfigvmextension

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"go.uber.org/zap"
	"go.yaml.in/yaml/v3"

	commonapi "github.com/odigos-io/odigos/common/api"
)

// action is the subset of a vm-agent action file entry this extension needs. Config is kept raw
// until the action type is known.
type action struct {
	Name     string    `yaml:"name"`
	Type     string    `yaml:"type"`
	Disabled bool      `yaml:"disabled"`
	Config   yaml.Node `yaml:"config"`

	path string
}

// collectorConfigBuilder folds all enabled actions of one type into the collector config.
type collectorConfigBuilder func(logger *zap.Logger, actions []action, cfg *commonapi.SourceCollectorConfig)

// collectorConfigBuilders maps each supported vm-agent action type (it must match the type vm-agent
// writes to actions.d) to its builder. Supporting a new action type means adding an entry here.
var collectorConfigBuilders = map[string]collectorConfigBuilder{
	"piiMasking":            buildPiiMasking,
	"urlTemplatization":     buildUrlTemplatization,
	"dbQueryTemplatization": buildDbQueryTemplatization,
	"inferDbAttributes":     buildInferDbAttributes,
}

// loadActions reads the enabled actions in dir whose type has a builder, grouped by type. Files that
// fail to parse are skipped so one broken file does not drop the others.
func loadActions(logger *zap.Logger, dir string) (map[string][]action, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}

	byType := make(map[string][]action)
	for _, entry := range entries {
		name := strings.ToLower(entry.Name())
		if entry.IsDir() || !(strings.HasSuffix(name, ".yaml") || strings.HasSuffix(name, ".yml")) {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		fileActions, err := readActionsFile(path)
		if err != nil {
			logger.Warn("skipping unreadable actions file", zap.String("path", path), zap.Error(err))
			continue
		}
		for _, a := range fileActions {
			if _, ok := collectorConfigBuilders[a.Type]; !ok || a.Disabled {
				continue
			}
			a.path = path
			byType[a.Type] = append(byType[a.Type], a)
		}
	}
	return byType, nil
}

// buildCollectorConfig runs every builder on its actions, so a type with no actions resets its field.
func buildCollectorConfig(logger *zap.Logger, actionsByType map[string][]action) *commonapi.SourceCollectorConfig {
	cfg := &commonapi.SourceCollectorConfig{}
	for actionType, build := range collectorConfigBuilders {
		build(logger, actionsByType[actionType], cfg)
	}
	return cfg
}

// readActionsFile accepts the same layouts as vm-agent: one or more YAML documents, each holding a
// single action or a list of actions. vm-agent itself writes a single list, but users can drop
// hand-written files into actions.d; if this reader accepted fewer layouts than vm-agent, vm-agent
// would add the processor while the extension silently ignored the action, so nothing would be masked.
func readActionsFile(path string) ([]action, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var out []action
	dec := yaml.NewDecoder(f)
	for {
		var node yaml.Node
		if err := dec.Decode(&node); err != nil {
			if errors.Is(err, io.EOF) {
				return out, nil
			}
			return nil, err
		}
		if node.Kind == yaml.DocumentNode {
			if len(node.Content) == 0 {
				continue
			}
			node = *node.Content[0]
		}
		switch node.Kind {
		case yaml.SequenceNode:
			var list []action
			if err := node.Decode(&list); err != nil {
				return nil, err
			}
			out = append(out, list...)
		case yaml.MappingNode:
			var a action
			if err := node.Decode(&a); err != nil {
				return nil, err
			}
			out = append(out, a)
		default:
			return nil, fmt.Errorf("unsupported YAML node kind %d", node.Kind)
		}
	}
}

// decodeActionConfigs decodes the config of each action into T, skipping (and logging) actions whose
// config does not decode.
func decodeActionConfigs[T any](logger *zap.Logger, actions []action) []T {
	out := make([]T, 0, len(actions))
	for _, a := range actions {
		var cfg T
		if err := a.Config.Decode(&cfg); err != nil {
			logger.Warn("skipping invalid action", zap.String("path", a.path), zap.String("action", a.Name), zap.String("type", a.Type), zap.Error(err))
			continue
		}
		out = append(out, cfg)
	}
	return out
}
