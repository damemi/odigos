package destinations

import (
	"embed"
	"io/fs"
	"strings"
	"sync"

	"gopkg.in/yaml.v3"
)

//go:embed data/*
var destsFS embed.FS

// array of all destinations configs
var loadedDestinations []Destination

// map from destination type to destination config object
var destinationsByType map[string]Destination

// loadOnce parses the embedded catalog the first time Load is called. It never changes at
// runtime, and callers such as the VM agent's collector config generator call Load on every
// regeneration.
var loadOnce = sync.OnceValue(func() error { return load(destsFS) })

func Load() error {
	return loadOnce()
}

func Get() []Destination {
	return loadedDestinations
}

func GetDestinationByType(destType string) (Destination, bool) {
	dest, ok := destinationsByType[destType]
	return dest, ok
}

func load(fsys fs.FS) error {
	var dests []Destination
	var destsByTypeMap = make(map[string]Destination)

	// load all files in the data directory
	files, err := fs.ReadDir(fsys, "data")
	if err != nil {
		return err
	}

	for _, file := range files {
		fileName := file.Name()

		// check if fileName ends with .yaml or .yml
		if !strings.HasSuffix(fileName, ".yaml") && !strings.HasSuffix(fileName, ".yml") {
			continue
		}

		// load each file
		bytesData, err := fs.ReadFile(fsys, "data/"+file.Name())
		if err != nil {
			return err
		}

		var dest Destination
		err = yaml.Unmarshal(bytesData, &dest)
		if err != nil {
			return err
		}

		destsByTypeMap[string(dest.Metadata.Type)] = dest
		dests = append(dests, dest)
	}

	destinationsByType = destsByTypeMap
	loadedDestinations = dests
	return nil
}
