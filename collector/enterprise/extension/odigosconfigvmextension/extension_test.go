package odigosconfigvmextension

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/collector/component/componenttest"
	"go.opentelemetry.io/collector/extension/extensiontest"
	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.uber.org/zap"

	commonapi "github.com/odigos-io/odigos/common/api"
	"github.com/odigos-io/odigos/common/api/actions"
	"github.com/odigos-io/odigos/common/collector"
)

type recordingCallback struct {
	mu   sync.Mutex
	last map[string]*commonapi.SourceCollectorConfig
}

func newRecordingCallback() *recordingCallback {
	return &recordingCallback{last: map[string]*commonapi.SourceCollectorConfig{}}
}

func (r *recordingCallback) OnSet(key string, cfg *commonapi.SourceCollectorConfig) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.last[key] = cfg
}

func (r *recordingCallback) OnDeleteKey(string) {}

func (r *recordingCallback) piiMasking(t *testing.T) *actions.PiiMaskingConfig {
	r.mu.Lock()
	defer r.mu.Unlock()
	cfg, ok := r.last[globalKey]
	require.True(t, ok, "OnSet was not called for the global key")
	return cfg.PiiMasking
}

func writeFile(t *testing.T, dir, name, content string) {
	t.Helper()
	require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644))
}

func startExtension(t *testing.T, dir string) collector.OdigosConfigExtension {
	t.Helper()
	cfg := &Config{ActionsDir: dir}
	require.NoError(t, cfg.Validate())
	ext, err := NewFactory().Create(context.Background(), extensiontest.NewNopSettings(Type), cfg)
	require.NoError(t, err)
	require.NoError(t, ext.Start(context.Background(), componenttest.NewNopHost()))
	t.Cleanup(func() { require.NoError(t, ext.Shutdown(context.Background())) })
	return ext.(collector.OdigosConfigExtension)
}

const piiActionCreditCard = `
- name: pii-cards
  type: piiMasking
  signals: [TRACES]
  config:
    piiCategories: [CREDIT_CARD]
    customRegexMaskings:
      - regex: 'id=([^&]+)'
`

func TestLoadMergesPiiMaskingActions(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "pii-cards.yaml", piiActionCreditCard)
	// Multi-document file mixing a single action, a disabled action and a non-PII action.
	writeFile(t, dir, "mixed.yml", `
name: pii-email
type: piiMasking
signals: [TRACES]
config:
  piiCategories: [EMAIL, CREDIT_CARD]
  customFormatMaskings:
    - lookupKey: customer_id
      dataFormat: json
---
- name: pii-disabled
  type: piiMasking
  disabled: true
  signals: [TRACES]
  config:
    piiCategories: [JWT]
- name: url-templatization
  type: urlTemplatization
  signals: [TRACES]
  config:
    templatizationRulesGroups:
      - templatizationRules:
          - template: /api/{id}
`)
	writeFile(t, dir, "broken.yaml", "- name: [unclosed")
	writeFile(t, dir, "pii-dup.yaml.dup", piiActionCreditCard)

	actionsByType, err := loadActions(zap.NewNop(), dir)
	require.NoError(t, err)
	cfg := buildCollectorConfig(zap.NewNop(), actionsByType)
	got := cfg.PiiMasking
	require.NotNil(t, got)
	assert.Equal(t, []actions.PiiCategory{actions.CreditCardMasking, actions.EmailMasking}, got.PiiCategories)
	assert.Equal(t, []actions.CustomFormatMasking{{LookupKey: "customer_id", DataFormat: "json"}}, got.CustomFormatMaskings)
	assert.Equal(t, []actions.CustomRegexMasking{{Regex: "id=([^&]+)"}}, got.CustomRegexMaskings)
}

func TestLoadWithoutPiiMaskingActionsReturnsNil(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "span-metrics.yaml", "- name: span-metrics\n  type: spanMetrics\n  signals: [TRACES]\n")

	actionsByType, err := loadActions(zap.NewNop(), dir)
	require.NoError(t, err)
	cfg := buildCollectorConfig(zap.NewNop(), actionsByType)
	assert.Nil(t, cfg.PiiMasking)
}

func TestRegisterDeliversCurrentConfigAndEveryResourceMapsToGlobalKey(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "pii-cards.yaml", piiActionCreditCard)
	ext := startExtension(t, dir)

	cb := newRecordingCallback()
	ext.RegisterWorkloadConfigCacheCallback(cb)
	require.NotNil(t, cb.piiMasking(t))
	assert.Equal(t, []actions.PiiCategory{actions.CreditCardMasking}, cb.piiMasking(t).PiiCategories)

	res := pcommon.NewResource()
	res.Attributes().PutStr("service.name", "any-service")
	key, err := ext.GetWorkloadCacheKey(res)
	require.NoError(t, err)
	assert.Equal(t, globalKey, key)
	assert.True(t, ext.IsActiveSource(res))
	assert.True(t, ext.WaitForCacheSync(context.Background()))
}

func TestActionsDirChangesArePushedToCallbacks(t *testing.T) {
	dir := t.TempDir()
	ext := startExtension(t, dir)

	cb := newRecordingCallback()
	ext.RegisterWorkloadConfigCacheCallback(cb)
	assert.Nil(t, cb.piiMasking(t))

	writeFile(t, dir, "pii-cards.yaml", piiActionCreditCard)
	require.Eventually(t, func() bool {
		cfg := cb.piiMasking(t)
		return cfg != nil && len(cfg.PiiCategories) == 1
	}, 5*time.Second, 50*time.Millisecond)

	require.NoError(t, os.Remove(filepath.Join(dir, "pii-cards.yaml")))
	require.Eventually(t, func() bool {
		return cb.piiMasking(t) == nil
	}, 5*time.Second, 50*time.Millisecond)
}

func TestStartFailsWhenActionsDirIsMissing(t *testing.T) {
	cfg := &Config{ActionsDir: filepath.Join(t.TempDir(), "missing")}
	ext, err := NewFactory().Create(context.Background(), extensiontest.NewNopSettings(Type), cfg)
	require.NoError(t, err)
	assert.Error(t, ext.Start(context.Background(), componenttest.NewNopHost()))
	require.NoError(t, ext.Shutdown(context.Background()))
}

func TestValidateRequiresActionsDir(t *testing.T) {
	assert.Error(t, (&Config{}).Validate())
}
