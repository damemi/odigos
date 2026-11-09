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

func (r *recordingCallback) urlTemplatization(t *testing.T) *actions.UrlTemplatizationConfig {
	r.mu.Lock()
	defer r.mu.Unlock()
	cfg, ok := r.last[globalKey]
	require.True(t, ok, "OnSet was not called for the global key")
	return cfg.UrlTemplatization
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
    templatizationRules: ["/api/{id}"]
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

const urlActionUsers = `
- name: url-users
  type: urlTemplatization
  signals: [TRACES]
  config:
    templatizationRules: ["/users/{id}", "/orders/{id}"]
`

func TestLoadMergesUrlTemplatizationActions(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "url-users.yaml", urlActionUsers)
	writeFile(t, dir, "url-more.yaml", `
- name: url-more
  type: urlTemplatization
  signals: [TRACES]
  config:
    templatizationRules: ["/users/{id}", "/carts/{id}"]
    default:
      skipPolicy:
        skipHttpStatusCodes: [404]
- name: url-skip
  type: urlTemplatization
  signals: [TRACES]
  config:
    default:
      skipPolicy:
        skipHttpStatusCodes: [401, 404]
- name: url-disabled
  type: urlTemplatization
  disabled: true
  signals: [TRACES]
  config:
    templatizationRules: ["/ignored/{id}"]
    default:
      disabled: true
`)
	writeFile(t, dir, "pii-cards.yaml", piiActionCreditCard)

	actionsByType, err := loadActions(zap.NewNop(), dir)
	require.NoError(t, err)
	cfg := buildCollectorConfig(zap.NewNop(), actionsByType)

	got := cfg.UrlTemplatization
	require.NotNil(t, got)
	assert.Equal(t, []string{"/carts/{id}", "/orders/{id}", "/users/{id}"}, got.Templates)
	require.NotNil(t, got.Default)
	require.NotNil(t, got.Default.SkipPolicy)
	assert.Equal(t, []int{401, 404}, got.Default.SkipPolicy.SkipHttpStatusCodes)
	require.NotNil(t, cfg.PiiMasking)
}

func TestMergeUrlTemplatizationConfigs(t *testing.T) {
	t.Run("no actions", func(t *testing.T) {
		assert.Nil(t, mergeUrlTemplatizationConfigs(nil))
	})

	t.Run("default templatization enabled when unset", func(t *testing.T) {
		got := mergeUrlTemplatizationConfigs([]actions.UrlTemplatizationConfig{{Templates: []string{"/a/{id}"}}})
		require.NotNil(t, got)
		assert.Equal(t, &actions.DefaultTemplatizationConfig{}, got.Default)
	})

	t.Run("disabled wins over skip policy", func(t *testing.T) {
		got := mergeUrlTemplatizationConfigs([]actions.UrlTemplatizationConfig{
			{Templates: []string{"/a/{id}"}, Default: &actions.DefaultTemplatizationConfig{SkipPolicy: &actions.DefaultTemplatizationSkipPolicyConfig{SkipForNonSuccessCodes: true}}},
			{Default: &actions.DefaultTemplatizationConfig{Disabled: true}},
		})
		require.NotNil(t, got)
		assert.Equal(t, []string{"/a/{id}"}, got.Templates)
		assert.Nil(t, got.Default)
	})

	t.Run("skip non success codes wins over status codes list", func(t *testing.T) {
		got := mergeUrlTemplatizationConfigs([]actions.UrlTemplatizationConfig{
			{Default: &actions.DefaultTemplatizationConfig{SkipPolicy: &actions.DefaultTemplatizationSkipPolicyConfig{SkipHttpStatusCodes: []int{404}}}},
			{Default: &actions.DefaultTemplatizationConfig{SkipPolicy: &actions.DefaultTemplatizationSkipPolicyConfig{SkipForNonSuccessCodes: true}}},
		})
		require.NotNil(t, got)
		assert.Equal(t, &actions.DefaultTemplatizationSkipPolicyConfig{SkipForNonSuccessCodes: true}, got.Default.SkipPolicy)
	})
}

func TestLoadWithoutUrlTemplatizationActionsReturnsNil(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "pii-cards.yaml", piiActionCreditCard)

	actionsByType, err := loadActions(zap.NewNop(), dir)
	require.NoError(t, err)
	cfg := buildCollectorConfig(zap.NewNop(), actionsByType)
	assert.Nil(t, cfg.UrlTemplatization)
}

func TestUrlTemplatizationChangesArePushedToCallbacks(t *testing.T) {
	dir := t.TempDir()
	ext := startExtension(t, dir)

	cb := newRecordingCallback()
	ext.RegisterWorkloadConfigCacheCallback(cb)
	assert.Nil(t, cb.urlTemplatization(t))

	writeFile(t, dir, "url-users.yaml", urlActionUsers)
	require.Eventually(t, func() bool {
		cfg := cb.urlTemplatization(t)
		return cfg != nil && len(cfg.Templates) == 2
	}, 5*time.Second, 50*time.Millisecond)

	require.NoError(t, os.Remove(filepath.Join(dir, "url-users.yaml")))
	require.Eventually(t, func() bool {
		return cb.urlTemplatization(t) == nil
	}, 5*time.Second, 50*time.Millisecond)
}

func TestLoadMergesDbActions(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "db.yaml", `
- name: db-literals
  type: dbQueryTemplatization
  signals: [TRACES]
  config:
    templatizeLiterals: true
- name: db-casts
  type: dbQueryTemplatization
  signals: [TRACES]
  config:
    removePostgresCastOperator: true
- name: db-infer
  type: inferDbAttributes
  signals: [TRACES]
`)

	actionsByType, err := loadActions(zap.NewNop(), dir)
	require.NoError(t, err)
	cfg := buildCollectorConfig(zap.NewNop(), actionsByType)

	assert.Equal(t, &actions.DbQueryTemplatizationConfig{TemplatizeLiterals: true, RemovePostgresCastOperator: true}, cfg.DbQueryTemplatization)
	assert.Equal(t, &actions.InferDbAttributesConfig{}, cfg.InferDbAttributes)
}

func TestLoadWithoutEnabledDbActionsReturnsNil(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "db.yaml", `
- name: db-no-options
  type: dbQueryTemplatization
  signals: [TRACES]
  config: {}
- name: db-infer-disabled
  type: inferDbAttributes
  disabled: true
  signals: [TRACES]
`)

	actionsByType, err := loadActions(zap.NewNop(), dir)
	require.NoError(t, err)
	cfg := buildCollectorConfig(zap.NewNop(), actionsByType)

	assert.Nil(t, cfg.DbQueryTemplatization)
	assert.Nil(t, cfg.InferDbAttributes)
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
