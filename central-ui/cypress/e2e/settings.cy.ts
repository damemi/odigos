import { CONFIG_MAPS, DATA_IDS, NAMESPACES, PROXY, ROUTES, TEXTS } from '../constants';
import { awaitToast, cyExec, handleExceptions, interceptGraphql, signInAndSelectProxy, visitPage, waitForGraphqlOperation } from '../functions';

const namespace = NAMESPACES.ODIGOS;
const testClusterName = 'cypress-e2e-test';
let originalClusterName = '';

// Baseline clusterName for assertions / cancel revert. Prefers the value
// captured from the EffectiveConfig ConfigMap; falls back to the proxy's
// known clusterName when the CM parse is empty.
const baselineClusterName = () => originalClusterName || PROXY.NAME || 'cluster';

// Source-of-truth readiness for the settings form: the visible clusterName
// input must show the effective value. Schema-driven fields can mount empty
// before EffectiveConfig binds; waiting on a GraphQL alias alone is not
// enough (and was wrong when `@gql` still held a stale overview
// GetEffectiveConfig from beforeEach bootstrap).
const waitForClusterNameField = (expected?: string) => {
  const want = expected ?? baselineClusterName();
  cy.get(DATA_IDS.SETTINGS_FIELD('clusterName'), { timeout: 30000 })
    .filter(':visible')
    .should('have.length.at.least', 1)
    .first()
    .should(($el) => {
      const val = String($el.val() ?? '').trim();
      expect(val, `clusterName hydrated to "${want}"`).to.eq(want);
    });
};

// After visit(/settings), wait for this page's GetEffectiveConfig (intercept
// is armed only after auth/proxy bootstrap, so the alias is not polluted),
// then wait for the form to bind values.
const waitForSettingsHydrated = (expectedClusterName?: string) => {
  waitForGraphqlOperation('GetEffectiveConfig');
  waitForClusterNameField(expectedClusterName);
};

// ── Schema compatibility ──────────────────────────────────────────────────
//
// The settings page renders fields based on whatever `configYamls.fields[i].helmValuePath`
// the proxy backend returns. The central-ui CI workflow installs the proxy from
// `odigos-io/odigos@${BASE_BRANCH}` (i.e. main), so when the corresponding OSS PR
// hasn't merged yet the proxy still publishes the legacy *flat* helmValuePaths
// (e.g. `rollbackGraceTime`) and is missing newer fields entirely (e.g. `sampling.*`).
//
// Rather than block this PR's CI on the OSS PR landing first, every field operation
// here is dual-aware:
//   • Try the new (nested) path; fall back to the legacy path if that's what's rendered.
//   • Silently skip operations on fields the schema doesn't expose at all.
// We track every field we *actually* set in `setFieldKeys` so the YAML / refresh
// assertions later only check values that were really written.
//
// Once the OSS PR is merged into main, the proxy will publish the new schema and
// every field will resolve to its new path; the legacy fallbacks become dead
// branches but cause no harm.

// Map of new (nested) path → legacy (flat) path. If the proxy renders neither,
// the field is skipped entirely.
const LEGACY_FIELD_PATHS: Record<string, string> = {
  'autoRollback.disabled': 'rollbackDisabled',
  'autoRollback.graceTime': 'rollbackGraceTime',
  'autoRollback.stabilityWindowTime': 'rollbackStabilityWindow',
  'instrumentor.mountMethod': 'mountMethod',
  'instrumentor.agentEnvVarsInjectionMethod': 'agentEnvVarsInjectionMethod',
  'instrumentor.checkDeviceHealthBeforeInjection': 'checkDeviceHealthBeforeInjection',
  'karpenter.enabled': 'karpenterEnabled',
  // pre-v1.24 stored this as a flat boolean, not an `{enabled: bool}` object,
  // so the legacy data-id is just `allowConcurrentAgents`.
  'allowConcurrentAgents.enabled': 'allowConcurrentAgents',
};

// Tracks logical (canonical/new) field keys that were successfully exercised by
// the update test. Subsequent YAML / refresh assertions consult this so they
// don't assert on values that were never written because the proxy schema
// didn't expose the field.
const setFieldKeys = new Set<string>();

// Tracks dot-paths actually returned (with a non-null/non-undefined value) by
// the proxy's `EffectiveConfig` response on the most recent page load.
// Populated by `collectEffectiveConfigKeys` and consulted by the refresh-time
// `verify*` helpers below.
//
// The legacy `setFieldKeys` gate alone isn't sufficient: a field can be
// publishable by `configYamls` (so it renders, accepts input, and is saved
// to `odigos-local-ui-config`) yet absent from the proxy's GraphQL schema for
// `EffectiveConfig` — which means the saved value never round-trips back into
// the form on refresh. The canonical example today is
// `rollout.maxConcurrentRollouts`: v1.24 OSS UI's `RolloutConfig` schema
// doesn't include it (it was added in the OSS PR shipping alongside this one),
// so Central UI's v1.24 versioned `GetEffectiveConfig` query doesn't request
// it. Once the proxy is on a release that includes the field, it will appear
// in `effectiveConfigKeys` automatically and the verify will start running —
// no test edits needed.
const effectiveConfigKeys = new Set<string>();

// Walks the `effectiveConfig` object from a captured `GetEffectiveConfig`
// response and populates `effectiveConfigKeys` with the dot-paths that have
// non-null/non-undefined leaf values. Arrays count as a single leaf value
// (their contents aren't expanded).
//
// The wire shape is `{ data: { remoteFetch: '<json string>' } }` because the
// query is forwarded through central-backend's `remoteFetch` resolver; the
// inner string parses to the regular Apollo response shape
// `{ data: { effectiveConfig: { ... } } }`.
// eslint-disable-next-line @typescript-eslint/no-explicit-any
const collectEffectiveConfigKeys = (interception: any) => {
  effectiveConfigKeys.clear();
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  let inner: any;
  try {
    const data = interception?.response?.body?.data;
    const remoteFetchStr = data?.remoteFetch;
    inner = typeof remoteFetchStr === 'string' ? JSON.parse(remoteFetchStr) : data;
  } catch {
    return;
  }
  const ec = inner?.data?.effectiveConfig;
  if (!ec || typeof ec !== 'object') return;
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  const walk = (obj: any, prefix: string) => {
    Object.entries(obj).forEach(([k, v]) => {
      if (v === null || v === undefined) return;
      const path = prefix ? `${prefix}.${k}` : k;
      if (typeof v === 'object' && !Array.isArray(v)) {
        walk(v, path);
      } else {
        effectiveConfigKeys.add(path);
      }
    });
  };
  walk(ec, '');
};

// True when a field's saved value is expected to round-trip via the current
// proxy's `EffectiveConfig` response — i.e. either we haven't captured the
// response yet (`size === 0`, defensive default) OR the path is present in
// the captured response. Used as the second gate (after `setFieldKeys`) on
// refresh-time `verify*` helpers.
const fieldRoundTrips = (fieldPath: string) => effectiveConfigKeys.size === 0 || effectiveConfigKeys.has(fieldPath);

const fieldExists = ($body: JQuery<HTMLElement>, dataId: string) => $body.find(`[data-id="${dataId}"]`).length > 0;

// Resolves `fieldPath` to whichever path the proxy actually rendered:
//   1. exact `fieldPath` (the new/canonical path) → use it
//   2. legacy fallback from `LEGACY_FIELD_PATHS`   → use that
//   3. neither rendered → skip the operation, log a notice
//
// `extraDataIdSuffix` lets time inputs probe `${fieldPath}-unit` instead of
// `${fieldPath}` (both must be rendered together when they exist).
const withFieldPath = (fieldPath: string, extraDataIdSuffix: string, fn: (resolvedPath: string) => void) => {
  cy.get('body').then(($body) => {
    const probe = (p: string) => fieldExists($body, `${p}${extraDataIdSuffix}`);

    if (probe(fieldPath)) {
      setFieldKeys.add(fieldPath);
      fn(fieldPath);
      return;
    }

    const legacy = LEGACY_FIELD_PATHS[fieldPath];
    if (legacy && probe(legacy)) {
      setFieldKeys.add(fieldPath);
      fn(legacy);
      return;
    }

    cy.log(`[settings] skip "${fieldPath}" — not rendered by current proxy schema`);
  });
};

const getConfigMapYaml = (configMapName: string, callback: (yaml: string) => void) => {
  cyExec(`kubectl get configmap ${configMapName} -n ${namespace} -o jsonpath='{.data.config\\.yaml}'`).then(({ stdout }) => {
    callback(stdout);
  });
};

const clickToggle = (fieldPath: string) => {
  withFieldPath(fieldPath, '', (path) => {
    cy.get(DATA_IDS.SETTINGS_FIELD(path)).children().first().click();
  });
};

const setInput = (fieldPath: string, value: string) => {
  withFieldPath(fieldPath, '', (path) => {
    cy.get(DATA_IDS.SETTINGS_FIELD(path)).click().focused().clear().type(value);
  });
};

// Time fields render an input on the left and a unit dropdown on the right.
// The DOM exposes the number input under the field path, the unit dropdown
// input under `${fieldPath}-unit`, and each dropdown option as
// `[data-id="option-${unitId}"]`. The combined value (e.g. "60s") is saved.
// The dropdown trigger is a read-only `<input>` whose `.value` is the joined
// option labels (e.g. "seconds") — no "Selected: " prefix is applied. We use
// exact data-id matching for option selection to avoid substring collisions
// (e.g. selecting "option-s" would also match "option-ms").
const TIME_UNIT_LABELS: Record<string, string> = {
  ms: 'milliseconds',
  s: 'seconds',
  m: 'minutes',
  h: 'hours',
};

const setTimeInput = (fieldPath: string, value: string) => {
  const match = value.match(/^(\d+)\s*(ms|s|m|h)$/i);
  if (!match) throw new Error(`setTimeInput: invalid time value "${value}"`);
  const [, num, unit] = match;
  const unitId = unit.toLowerCase();

  // Probe the `-unit` suffix because legacy proxy schemas (e.g. v1.24.2)
  // render `autoRollback.graceTime` as a plain `input` componentType — the
  // `data-id` for the field exists, but `${fieldPath}-unit` does not. Without
  // this gate the test would hang on `cy.get('[data-id="…-unit"]')` and
  // cascade into "Save never clicked → odigos-local-ui-config never created"
  // failures across the rest of the suite.
  withFieldPath(fieldPath, '-unit', (path) => {
    cy.get(DATA_IDS.SETTINGS_FIELD(path)).scrollIntoView().click().focused().clear().type(num);
    cy.get(DATA_IDS.SETTINGS_FIELD(`${path}-unit`))
      .scrollIntoView()
      .click({ force: true });
    // Portal-rendered option may land outside the viewport; force-click skips actionability checks.
    // The previous dropdown's list can still be closing, and the open one is the last in the DOM.
    cy.get(`[data-id="option-${unitId}"]`).last().click({ force: true });
  });
};

const selectDropdownOption = (fieldPath: string, optionLabel: string) => {
  withFieldPath(fieldPath, '', (path) => {
    cy.get(DATA_IDS.SETTINGS_FIELD(path)).scrollIntoView().click({ force: true });
    // Portal-rendered option may land outside the viewport; force-click skips actionability checks.
    // The previous dropdown's list can still be closing, and the open one is the last in the DOM.
    cy.get(`[data-id="option-${optionLabel}"]`).last().click({ force: true });
  });
};

const addMultiInputValue = (fieldPath: string, value: string) => {
  withFieldPath(fieldPath, '', (path) => {
    cy.get(DATA_IDS.SETTINGS_FIELD(path)).find('button').first().click();
    cy.get(DATA_IDS.SETTINGS_FIELD(path)).find('input').last().clear().type(value);
  });
};

const verifyInput = (fieldPath: string, expectedValue: string) => {
  if (!setFieldKeys.has(fieldPath)) return;
  if (!fieldRoundTrips(fieldPath)) return;
  withFieldPath(fieldPath, '', (path) => {
    cy.get(DATA_IDS.SETTINGS_FIELD(path)).should('have.value', expectedValue);
  });
};

const verifyTimeInput = (fieldPath: string, expectedValue: string) => {
  if (!setFieldKeys.has(fieldPath)) return;
  if (!fieldRoundTrips(fieldPath)) return;
  const match = expectedValue.match(/^(\d+)\s*(ms|s|m|h)$/i);
  if (!match) throw new Error(`verifyTimeInput: invalid time value "${expectedValue}"`);
  const [, num, unit] = match;
  const unitLabel = TIME_UNIT_LABELS[unit.toLowerCase()];

  withFieldPath(fieldPath, '-unit', (path) => {
    cy.get(DATA_IDS.SETTINGS_FIELD(path)).should('have.value', num);
    cy.get(DATA_IDS.SETTINGS_FIELD(`${path}-unit`)).should('have.value', unitLabel);
  });
};

const verifyDropdown = (fieldPath: string, expectedLabel: string) => {
  if (!setFieldKeys.has(fieldPath)) return;
  if (!fieldRoundTrips(fieldPath)) return;
  withFieldPath(fieldPath, '', (path) => {
    cy.get(DATA_IDS.SETTINGS_FIELD(path)).should('have.value', expectedLabel);
  });
};

// Toggle exposes its boolean state via the `data-toggle-value` attribute on the
// element with `data-id={fieldPath}`. Using the attribute keeps the assertion
// independent of styling/layout. Skips silently if the field wasn't actually
// exercised (e.g. the proxy schema didn't render it).
const verifyToggle = (fieldPath: string, expectedValue: boolean) => {
  if (!setFieldKeys.has(fieldPath)) return;
  if (!fieldRoundTrips(fieldPath)) return;
  withFieldPath(fieldPath, '', (path) => {
    cy.get(DATA_IDS.SETTINGS_FIELD(path)).should('have.attr', 'data-toggle-value', expectedValue ? 'true' : 'false');
  });
};

const verifyMultiInputContains = (fieldPath: string, expectedValue: string) => {
  if (!setFieldKeys.has(fieldPath)) return;
  if (!fieldRoundTrips(fieldPath)) return;
  withFieldPath(fieldPath, '', (path) => {
    cy.get(DATA_IDS.SETTINGS_FIELD(path))
      .find('input')
      .should(($inputs) => {
        const values = $inputs.toArray().map((el) => (el as HTMLInputElement).value);
        expect(values).to.include(expectedValue);
      });
  });
};

// YAML assertion helpers — only assert when the corresponding logical field
// was actually set in the update test. Keeps the YAML checks honest when the
// proxy schema lacks the field entirely.
const yamlExpectIfSet = (fieldKey: string, yaml: string, ...substrings: string[]) => {
  if (!setFieldKeys.has(fieldKey)) return;
  substrings.forEach((s) => expect(yaml).to.contain(s));
};

const yamlExpectAnyIfSet = (fieldKey: string, yaml: string, alternatives: string[]) => {
  if (!setFieldKeys.has(fieldKey)) return;
  expect(yaml).to.satisfy((s: string) => alternatives.some((alt) => s.includes(alt)));
};

const yamlExpectAbsentIfSet = (fieldKey: string, yaml: string, ...substrings: string[]) => {
  if (!setFieldKeys.has(fieldKey)) return;
  substrings.forEach((s) => expect(yaml).to.not.contain(s));
};

describe('Settings CRUD (via Central UI → Proxy)', () => {
  beforeEach(() => {
    handleExceptions();
    signInAndSelectProxy();
    // After bootstrap — never before. See interceptGraphql().
    interceptGraphql();
  });

  // ── Setup ─────────────────────────────────────────────────────────────────

  it('Should capture initial state from the cluster', () => {
    cyExec(`kubectl delete configmap ${CONFIG_MAPS.LOCAL_UI_CONFIG} -n ${namespace}`, { failOnNonZeroExit: false });
    cy.wait(10000);

    getConfigMapYaml(CONFIG_MAPS.EFFECTIVE_CONFIG, (yaml) => {
      const match = yaml.match(/clusterName:\s*(.+)/);
      // Strip trailing comments / quotes so we compare apples-to-apples with the input value.
      originalClusterName = match ? match[1].trim().split(/\s+#/)[0].replace(/^["']|["']$/g, '') : '';
    });
  });

  // ── Read ──────────────────────────────────────────────────────────────────

  it('Should render config sections from the cluster', () => {
    visitPage(ROUTES.SETTINGS, () => {
      waitForSettingsHydrated();
      cy.contains('General', { timeout: 30000 }).should('be.visible');
      cy.contains('Instrumentation').should('exist');
      cy.contains('Rollout & Rollback').should('exist');
      cy.contains('Namespaces & Filtering').should('exist');
      cy.contains('Component Log Levels').should('exist');
      cy.contains('Sampling').should('exist');
      cy.contains('Advanced').should('exist');
      // The effective config YAML moved behind a "Show Config YAML" toolbar action
      // that opens a drawer titled "Effective Config YAML".
      // Do not `.should('exist').click()` — that settles the subject and breaks
      // re-resolution when settings re-renders (detached DOM mid-click flake).
      cy.contains('Show Config YAML', { timeout: 30000 }).click();
      cy.contains('Effective Config YAML').should('exist');
    });
  });

  it('Should render the clusterName field with its current value', () => {
    visitPage(ROUTES.SETTINGS, () => {
      waitForSettingsHydrated();
    });
  });

  it('Should not show the Save/Cancel island when no changes are made', () => {
    visitPage(ROUTES.SETTINGS, () => {
      waitForSettingsHydrated();
      cy.get(DATA_IDS.SETTINGS_SAVE).should('not.exist');
      cy.get(DATA_IDS.SETTINGS_CANCEL).should('not.exist');
    });
  });

  // ── Update + Cancel ───────────────────────────────────────────────────────

  it('Should show the Save/Cancel island when a field is modified, and revert on Cancel', () => {
    visitPage(ROUTES.SETTINGS, () => {
      const baseline = baselineClusterName();
      const cancelTestValue = baseline + '-cancel-test';

      waitForSettingsHydrated(baseline);
      cy.get(DATA_IDS.SETTINGS_FIELD('clusterName')).click().focused().clear().type(cancelTestValue);

      cy.get(DATA_IDS.SETTINGS_SAVE).should('be.visible');
      cy.get(DATA_IDS.SETTINGS_CANCEL).should('be.visible');

      cy.get(DATA_IDS.SETTINGS_CANCEL).click();

      cy.get(DATA_IDS.SETTINGS_FIELD('clusterName')).should('have.value', baseline);
      cy.get(DATA_IDS.SETTINGS_SAVE).should('not.exist');
    });
  });

  // ── Update ALL non-helm-only, non-enterprise-only fields + Save ─────────

  it('Should update all non-helm-only, non-enterprise-only fields and save successfully', () => {
    visitPage(ROUTES.SETTINGS, () => {
      waitForSettingsHydrated();

      // ─ General ─
      setInput('clusterName', testClusterName);
      // telemetryEnabled is Helm-only since v1.40 (an overlay can't turn it off), so it isn't edited here.

      // ─ Instrumentation ─
      selectDropdownOption('instrumentor.agentEnvVarsInjectionMethod', 'pod-manifest');
      clickToggle('allowConcurrentAgents.enabled');
      clickToggle('instrumentor.checkDeviceHealthBeforeInjection');

      // ─ Rollout & Rollback ─
      clickToggle('rollout.automaticRolloutDisabled');
      setInput('rollout.maxConcurrentRollouts', '5');
      clickToggle('autoRollback.disabled');
      setTimeInput('autoRollback.graceTime', '60s');
      setTimeInput('autoRollback.stabilityWindowTime', '120s');

      // ─ Namespaces & Filtering ─
      addMultiInputValue('ignoredNamespaces', 'cypress-test-ns');
      addMultiInputValue('ignoredContainers', 'cypress-test-container');
      clickToggle('ignoreOdigosNamespace');

      // ─ Component Log Levels ─
      selectDropdownOption('componentLogLevels.default', 'debug');
      selectDropdownOption('componentLogLevels.autoscaler', 'debug');
      selectDropdownOption('componentLogLevels.scheduler', 'debug');
      selectDropdownOption('componentLogLevels.instrumentor', 'debug');
      selectDropdownOption('componentLogLevels.odiglet', 'debug');
      selectDropdownOption('componentLogLevels.deviceplugin', 'debug');
      selectDropdownOption('componentLogLevels.ui', 'debug');
      selectDropdownOption('componentLogLevels.collector', 'debug');

      // ─ Sampling ─
      clickToggle('sampling.dryRun');
      clickToggle('sampling.spanSamplingAttributes.disabled');
      clickToggle('sampling.spanSamplingAttributes.samplingCategoryDisabled');
      clickToggle('sampling.spanSamplingAttributes.traceDecidingRuleDisabled');
      clickToggle('sampling.spanSamplingAttributes.spanDecisionAttributesDisabled');
      clickToggle('sampling.tailSampling.disabled');
      setTimeInput('sampling.tailSampling.traceAggregationWaitDuration', '45s');
      clickToggle('sampling.k8sHealthProbesSampling.enabled');
      setInput('sampling.k8sHealthProbesSampling.keepPercentage', '50');

      // ─ Save ─
      cy.get(DATA_IDS.SETTINGS_SAVE).should('be.visible').click();

      cy.contains('Save changes').click();

      // The toast holds the test until it did: ending it now would cancel the request with the page.
      awaitToast({ message: TEXTS.NOTIF_CONFIG_UPDATED });
    });
  });

  // ── Verify kubectl: local-ui-config ────────────────────────────────────

  it(`Should have written all changes to the ${CONFIG_MAPS.LOCAL_UI_CONFIG} ConfigMap`, () => {
    getConfigMapYaml(CONFIG_MAPS.LOCAL_UI_CONFIG, (yaml) => {
      // clusterName is unconditional — it's the first field set and required
      // for the rest of the suite to make sense; if it isn't here something
      // is fundamentally broken with the save flow.
      expect(yaml).to.contain(`clusterName: ${testClusterName}`);

      yamlExpectIfSet('instrumentor.agentEnvVarsInjectionMethod', yaml, 'agentEnvVarsInjectionMethod: pod-manifest');
      yamlExpectIfSet('allowConcurrentAgents.enabled', yaml, 'allowConcurrentAgents:');
      yamlExpectIfSet('instrumentor.checkDeviceHealthBeforeInjection', yaml, 'checkDeviceHealthBeforeInjection:');

      yamlExpectIfSet('rollout.automaticRolloutDisabled', yaml, 'automaticRolloutDisabled:');
      yamlExpectIfSet('rollout.maxConcurrentRollouts', yaml, 'maxConcurrentRollouts: 5');
      yamlExpectIfSet('autoRollback.disabled', yaml, 'rollbackDisabled:');
      yamlExpectAnyIfSet('autoRollback.graceTime', yaml, ['rollbackGraceTime: 60s', 'rollbackGraceTime: "60s"']);
      yamlExpectAnyIfSet('autoRollback.stabilityWindowTime', yaml, ['rollbackStabilityWindow: 120s', 'rollbackStabilityWindow: "120s"']);

      yamlExpectIfSet('ignoredNamespaces', yaml, 'cypress-test-ns');
      yamlExpectIfSet('ignoredContainers', yaml, 'cypress-test-container');
      yamlExpectIfSet('ignoreOdigosNamespace', yaml, 'ignoreOdigosNamespace:');

      // componentLogLevels: at least one level must have been set for the
      // section to appear in YAML — gate on the canonical `default` field
      // since the other levels are skipped together when the schema is sparse.
      yamlExpectIfSet('componentLogLevels.default', yaml, 'componentLogLevels:');

      yamlExpectIfSet('sampling.dryRun', yaml, 'sampling:', 'dryRun:');
      yamlExpectIfSet('sampling.spanSamplingAttributes.disabled', yaml, 'spanSamplingAttributes:');
      yamlExpectIfSet('sampling.tailSampling.disabled', yaml, 'tailSampling:');
      yamlExpectAnyIfSet('sampling.tailSampling.traceAggregationWaitDuration', yaml, ['traceAggregationWaitDuration: 45s', 'traceAggregationWaitDuration: "45s"']);
      yamlExpectIfSet('sampling.k8sHealthProbesSampling.enabled', yaml, 'k8sHealthProbesSampling:');
      yamlExpectIfSet('sampling.k8sHealthProbesSampling.keepPercentage', yaml, 'keepPercentage: 50');
    });
  });

  it(`Should have reconciled changes into the ${CONFIG_MAPS.EFFECTIVE_CONFIG} ConfigMap`, () => {
    cy.wait(5000).then(() => {
      getConfigMapYaml(CONFIG_MAPS.EFFECTIVE_CONFIG, (yaml) => {
        expect(yaml).to.contain(`clusterName: ${testClusterName}`);

        yamlExpectIfSet('instrumentor.agentEnvVarsInjectionMethod', yaml, 'agentEnvVarsInjectionMethod: pod-manifest');
        yamlExpectIfSet('allowConcurrentAgents.enabled', yaml, 'allowConcurrentAgents:');
        yamlExpectIfSet('instrumentor.checkDeviceHealthBeforeInjection', yaml, 'checkDeviceHealthBeforeInjection:');

        yamlExpectIfSet('rollout.automaticRolloutDisabled', yaml, 'automaticRolloutDisabled:');
        yamlExpectIfSet('rollout.maxConcurrentRollouts', yaml, 'maxConcurrentRollouts: 5');
        yamlExpectIfSet('autoRollback.disabled', yaml, 'rollbackDisabled:');
        yamlExpectAnyIfSet('autoRollback.graceTime', yaml, ['rollbackGraceTime: 60s', 'rollbackGraceTime: "60s"']);
        yamlExpectAnyIfSet('autoRollback.stabilityWindowTime', yaml, ['rollbackStabilityWindow: 120s', 'rollbackStabilityWindow: "120s"']);

        yamlExpectIfSet('ignoredNamespaces', yaml, 'cypress-test-ns');
        yamlExpectIfSet('ignoredContainers', yaml, 'cypress-test-container');

        yamlExpectAnyIfSet('sampling.tailSampling.traceAggregationWaitDuration', yaml, ['traceAggregationWaitDuration: 45s', 'traceAggregationWaitDuration: "45s"']);
        yamlExpectIfSet('sampling.k8sHealthProbesSampling.keepPercentage', yaml, 'keepPercentage: 50');
      });
    });
  });

  // ── Verify UI values after page refresh ────────────────────────────────

  it('Should display saved values in UI fields after page refresh', () => {
    visitPage(ROUTES.SETTINGS, () => {
      waitForGraphqlOperation('GetEffectiveConfig').then((interception) => {
        // Capture which fields the proxy actually returned so the verifiers
        // below can skip ones that don't round-trip on the current schema
        // (e.g. `rollout.maxConcurrentRollouts` on legacy v1.24 proxies).
        collectEffectiveConfigKeys(interception);
      });
      waitForClusterNameField(testClusterName);

      cy.get(DATA_IDS.SETTINGS_SAVE).should('not.exist');
      cy.get(DATA_IDS.SETTINGS_CANCEL).should('not.exist');

      // Each verifier silently skips when the proxy schema didn't expose the
      // field during the update (see `setFieldKeys` / `withFieldPath` above),
      // or when the proxy's EffectiveConfig response doesn't carry the field
      // back (see `effectiveConfigKeys` / `fieldRoundTrips`), so this block
      // is safe across legacy and current proxy versions.

      // ─ General ─
      verifyInput('clusterName', testClusterName);

      // ─ Instrumentation ─
      verifyDropdown('instrumentor.agentEnvVarsInjectionMethod', 'pod-manifest');
      verifyToggle('allowConcurrentAgents.enabled', true);
      verifyToggle('instrumentor.checkDeviceHealthBeforeInjection', true);

      // ─ Rollout & Rollback ─
      verifyToggle('rollout.automaticRolloutDisabled', true);
      verifyInput('rollout.maxConcurrentRollouts', '5');
      verifyToggle('autoRollback.disabled', true);
      verifyTimeInput('autoRollback.graceTime', '60s');
      verifyTimeInput('autoRollback.stabilityWindowTime', '120s');

      // ─ Namespaces & Filtering ─
      verifyMultiInputContains('ignoredNamespaces', 'cypress-test-ns');
      verifyMultiInputContains('ignoredContainers', 'cypress-test-container');
      verifyToggle('ignoreOdigosNamespace', false);

      // ─ Component Log Levels ─
      verifyDropdown('componentLogLevels.default', 'debug');
      verifyDropdown('componentLogLevels.autoscaler', 'debug');
      verifyDropdown('componentLogLevels.scheduler', 'debug');
      verifyDropdown('componentLogLevels.instrumentor', 'debug');
      verifyDropdown('componentLogLevels.odiglet', 'debug');
      verifyDropdown('componentLogLevels.deviceplugin', 'debug');
      verifyDropdown('componentLogLevels.ui', 'debug');
      verifyDropdown('componentLogLevels.collector', 'debug');

      // ─ Sampling ─
      verifyToggle('sampling.dryRun', true);
      verifyToggle('sampling.spanSamplingAttributes.disabled', true);
      verifyToggle('sampling.spanSamplingAttributes.samplingCategoryDisabled', true);
      verifyToggle('sampling.spanSamplingAttributes.traceDecidingRuleDisabled', true);
      verifyToggle('sampling.spanSamplingAttributes.spanDecisionAttributesDisabled', true);
      verifyToggle('sampling.tailSampling.disabled', true);
      verifyTimeInput('sampling.tailSampling.traceAggregationWaitDuration', '45s');
      verifyToggle('sampling.k8sHealthProbesSampling.enabled', true);
      verifyInput('sampling.k8sHealthProbesSampling.keepPercentage', '50');
    });
  });

  // ── Reset via UI ───────────────────────────────────────────────────────

  it('Should reset settings via the Reset button and confirm modal', () => {
    visitPage(ROUTES.SETTINGS, () => {
      waitForSettingsHydrated(testClusterName);
      // Reset sits in the toolbar's "more actions" menu.
      cy.get('[data-id=settings-more]').click();
      cy.get('[data-id=settings-reset]').click();

      // Confirm the reset warning modal
      cy.get(DATA_IDS.APPROVE).click();

      awaitToast({ message: TEXTS.NOTIF_CONFIG_RESET });
    });
  });

  it(`Should have cleared the ${CONFIG_MAPS.LOCAL_UI_CONFIG} ConfigMap after reset`, () => {
    cy.wait(5000).then(() => {
      getConfigMapYaml(CONFIG_MAPS.LOCAL_UI_CONFIG, (yaml) => {
        // clusterName is the only unconditional check (see the LOCAL_UI_CONFIG
        // assertion above); everything else depends on whether the proxy
        // schema actually exposed the corresponding field during the update.
        expect(yaml).to.not.contain(`clusterName: ${testClusterName}`);
        yamlExpectAbsentIfSet('instrumentor.agentEnvVarsInjectionMethod', yaml, 'agentEnvVarsInjectionMethod: pod-manifest');
        yamlExpectAbsentIfSet('ignoredNamespaces', yaml, 'cypress-test-ns');
        yamlExpectAbsentIfSet('ignoredContainers', yaml, 'cypress-test-container');
        yamlExpectAbsentIfSet('rollout.maxConcurrentRollouts', yaml, 'maxConcurrentRollouts: 5');
        yamlExpectAbsentIfSet('sampling.k8sHealthProbesSampling.keepPercentage', yaml, 'keepPercentage: 50');
        yamlExpectAbsentIfSet('allowConcurrentAgents.enabled', yaml, 'allowConcurrentAgents:');
      });
    });
  });

  it(`Should have reconciled the reset into the ${CONFIG_MAPS.EFFECTIVE_CONFIG} ConfigMap`, () => {
    cy.wait(5000).then(() => {
      getConfigMapYaml(CONFIG_MAPS.EFFECTIVE_CONFIG, (yaml) => {
        expect(yaml).to.not.contain(`clusterName: ${testClusterName}`);
        yamlExpectAbsentIfSet('instrumentor.agentEnvVarsInjectionMethod', yaml, 'agentEnvVarsInjectionMethod: pod-manifest');
        yamlExpectAbsentIfSet('ignoredNamespaces', yaml, 'cypress-test-ns');
        yamlExpectAbsentIfSet('ignoredContainers', yaml, 'cypress-test-container');
        yamlExpectAbsentIfSet('rollout.maxConcurrentRollouts', yaml, 'maxConcurrentRollouts: 5');
        yamlExpectAbsentIfSet('sampling.k8sHealthProbesSampling.keepPercentage', yaml, 'keepPercentage: 50');
      });
    });
  });
});
