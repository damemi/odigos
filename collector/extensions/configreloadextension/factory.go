//go:generate mdatagen metadata.yaml
package configreloadextension

import (
	"context"
	"fmt"
	"log"
	"os"
	"syscall"

	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/extension"

	"github.com/fsnotify/fsnotify"
)

type configReloader struct {
	watcher *fsnotify.Watcher
	pid     int
}

func NewFactory() extension.Factory {
	return extension.NewFactory(
		component.MustNewType("configreload"),
		createDefaultConfig,
		createExtension,
		component.StabilityLevelAlpha,
	)
}

func createDefaultConfig() component.Config {
	return &Config{}
}

func createExtension(ctx context.Context, s extension.Settings, cfg component.Config) (extension.Extension, error) {
	config := cfg.(*Config)
	cr := &configReloader{
		pid: os.Getpid(),
	}

	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}
	err = watcher.Add(config.File)
	if err != nil {
		return nil, err
	}
	cr.watcher = watcher

	return cr, nil
}

func (c *configReloader) Start(ctx context.Context, host component.Host) error {
	for {
		select {
		case event, ok := <-c.watcher.Events:
			if !ok {
				return fmt.Errorf("error watching event")
			}
			if event.Has(fsnotify.Write) {
				log.Println("modified file:", event.Name)
				err := syscall.Kill(c.pid, syscall.SIGHUP)
				if err != nil {
					return err
				}
			}
		case err, ok := <-c.watcher.Errors:
			if !ok {
				return fmt.Errorf("error received from config watcher: %+v", err)
			}
		}
	}
}

func (c *configReloader) Shutdown(ctx context.Context) error {
	return c.watcher.Close()
}
