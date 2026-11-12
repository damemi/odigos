package actions

import (
	"embed"
	"strings"
	"sync"

	"gopkg.in/yaml.v3"
)

//go:embed data/*
var actionsFS embed.FS

// array of all action catalog configs
var loadedActions []Action

// map from action type to action catalog config object
var actionsByType map[string]Action

// loadOnce parses the action catalog the first time Load is called; it is embedded and never
// changes at runtime.
var loadOnce = sync.OnceValue(func() error { return load(actionsFS) })

func Load() error {
	return loadOnce()
}

func Get() []Action {
	return loadedActions
}

func GetActionByType(actionType string) (Action, bool) {
	action, ok := actionsByType[actionType]
	return action, ok
}

func load(fs embed.FS) error {
	var acts []Action
	var actsByTypeMap = make(map[string]Action)

	files, err := fs.ReadDir("data")
	if err != nil {
		return err
	}

	for _, file := range files {
		fileName := file.Name()

		if !strings.HasSuffix(fileName, ".yaml") && !strings.HasSuffix(fileName, ".yml") {
			continue
		}

		bytesData, err := fs.ReadFile("data/" + file.Name())
		if err != nil {
			return err
		}

		var act Action
		err = yaml.Unmarshal(bytesData, &act)
		if err != nil {
			return err
		}

		actsByTypeMap[act.Metadata.Type] = act
		acts = append(acts, act)
	}

	actionsByType = actsByTypeMap
	loadedActions = acts
	return nil
}
