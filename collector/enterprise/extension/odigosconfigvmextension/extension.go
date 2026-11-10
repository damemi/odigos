package odigosconfigvmextension

import (
	"context"
	"fmt"
	"reflect"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.uber.org/zap"

	commonapi "github.com/odigos-io/odigos/common/api"
	"github.com/odigos-io/odigos/common/collector"
)

// globalKey is the single workload cache key: on a VM the config applies to every resource.
// This holds while VM actions (currently only PII masking) have no scopes; per-source keys may replace it.
const globalKey = "global"

// reloadDebounce coalesces the burst of events a single file write produces.
const reloadDebounce = 250 * time.Millisecond

// odigosConfigVM serves the collector config built from the vm-agent actions directory to every
// resource, and pushes updates to registered processors whenever the directory changes.
type odigosConfigVM struct {
	logger     *zap.Logger
	actionsDir string

	// mu guards globalConfig and callbacks; it is held while notifying so callbacks see updates in order.
	mu           sync.Mutex
	globalConfig *commonapi.SourceCollectorConfig
	callbacks    map[collector.WorkloadConfigCacheCallback]struct{}

	watcher *fsnotify.Watcher
	cancel  context.CancelFunc
	done    chan struct{}
}

var _ collector.OdigosConfigExtension = (*odigosConfigVM)(nil)

func newOdigosConfigVM(logger *zap.Logger, actionsDir string) *odigosConfigVM {
	return &odigosConfigVM{
		logger:       logger,
		actionsDir:   actionsDir,
		globalConfig: &commonapi.SourceCollectorConfig{},
		callbacks:    make(map[collector.WorkloadConfigCacheCallback]struct{}),
	}
}

func (o *odigosConfigVM) configFromActionsDir() (*commonapi.SourceCollectorConfig, error) {
	actionsByType, err := loadActions(o.logger, o.actionsDir)
	if err != nil {
		return nil, err
	}
	return buildCollectorConfig(o.logger, actionsByType), nil
}

func (o *odigosConfigVM) Start(ctx context.Context, _ component.Host) error {
	cfg, err := o.configFromActionsDir()
	if err != nil {
		return fmt.Errorf("failed to read actions dir %q: %w", o.actionsDir, err)
	}
	o.mu.Lock()
	o.globalConfig = cfg
	o.mu.Unlock()

	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return fmt.Errorf("failed to create actions dir watcher: %w", err)
	}
	if err := watcher.Add(o.actionsDir); err != nil {
		watcher.Close()
		return fmt.Errorf("failed to watch actions dir %q: %w", o.actionsDir, err)
	}
	o.watcher = watcher

	// The Start context must not be used for long-running work, so the watch loop gets its own.
	watchCtx, cancel := context.WithCancel(context.Background())
	o.cancel = cancel
	o.done = make(chan struct{})
	go o.watch(watchCtx)
	return nil
}

func (o *odigosConfigVM) Shutdown(context.Context) error {
	if o.cancel != nil {
		o.cancel()
		<-o.done
	}
	if o.watcher != nil {
		o.watcher.Close()
	}
	o.mu.Lock()
	clear(o.callbacks)
	o.mu.Unlock()
	return nil
}

func (o *odigosConfigVM) watch(ctx context.Context) {
	defer close(o.done)

	timer := time.NewTimer(reloadDebounce)
	timer.Stop()
	defer timer.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case _, ok := <-o.watcher.Events:
			if !ok {
				return
			}
			timer.Reset(reloadDebounce)
		case err, ok := <-o.watcher.Errors:
			if !ok {
				return
			}
			o.logger.Warn("actions dir watcher error", zap.Error(err))
		case <-timer.C:
			o.reload()
		}
	}
}

// reload re-reads the actions dir and notifies callbacks only when the merged config changed.
// On a read error the previous config is kept.
func (o *odigosConfigVM) reload() {
	cfg, err := o.configFromActionsDir()
	if err != nil {
		o.logger.Warn("failed to reload actions dir; keeping previous config", zap.String("dir", o.actionsDir), zap.Error(err))
		return
	}

	o.mu.Lock()
	defer o.mu.Unlock()
	if reflect.DeepEqual(o.globalConfig, cfg) {
		return
	}
	o.globalConfig = cfg
	o.logger.Info("collector config updated from actions dir", zap.String("dir", o.actionsDir))
	for cb := range o.callbacks {
		cb.OnSet(globalKey, o.globalConfig)
	}
}

func (o *odigosConfigVM) currentConfig() *commonapi.SourceCollectorConfig {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.globalConfig
}

func (o *odigosConfigVM) GetFromResource(pcommon.Resource) (*commonapi.SourceCollectorConfig, bool) {
	return o.currentConfig(), true
}

func (o *odigosConfigVM) IsActiveSource(pcommon.Resource) bool {
	return true
}

func (o *odigosConfigVM) GetWorkloadCacheKey(pcommon.Resource) (string, error) {
	return globalKey, nil
}

func (o *odigosConfigVM) GetWorkloadIdentityFromResource(pcommon.Resource) (string, pcommon.Map, error) {
	return globalKey, pcommon.NewMap(), nil
}

// RegisterWorkloadConfigCacheCallback delivers the current config right away; later changes to the
// actions dir are delivered by reload.
func (o *odigosConfigVM) RegisterWorkloadConfigCacheCallback(cb collector.WorkloadConfigCacheCallback) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.callbacks[cb] = struct{}{}
	cb.OnSet(globalKey, o.globalConfig)
}

func (o *odigosConfigVM) UnregisterWorkloadConfigCacheCallback(cb collector.WorkloadConfigCacheCallback) {
	o.mu.Lock()
	defer o.mu.Unlock()
	delete(o.callbacks, cb)
}

// WaitForCacheSync returns immediately: Start loads the actions dir synchronously.
func (o *odigosConfigVM) WaitForCacheSync(context.Context) bool {
	return true
}

// GetDataStreamsForWorkload is part of the OdigosConfigExtension interface; data streams are not used on VMs.
func (o *odigosConfigVM) GetDataStreamsForWorkload(pcommon.Resource) ([]string, bool) {
	return nil, false
}
