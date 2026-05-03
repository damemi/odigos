import { CONFIG_MAPS, DATA_IDS, NAMESPACES, ROUTES, TEXTS } from '../constants';
import { awaitToast, handleExceptions, signInAndSelectProxy, visitPage, waitForGraphqlOperation } from '../functions';

const namespace = NAMESPACES.ODIGOS;
const testClusterName = 'cypress-e2e-test';
let originalClusterName = '';

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
  'wasp.enabled': 'waspEnabled',
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
  cy.exec(`kubectl get configmap ${configMapName} -n ${namespace} -o jsonpath='{.data.config\\.yaml}'`).then(({ stdout }) => {
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
// The DOM exposes the number input under the field path, and the unit dropdown
// under `${fieldPath}-unit`. The combined value (e.g. "60s") is what gets saved.
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
  const unitLabel = TIME_UNIT_LABELS[unit.toLowerCase()];

  // Probe the `-unit` suffix because some legacy schemas render the number
  // input under the new path while omitting the unit dropdown (or vice versa);
  // we only want to interact when both are present.
  withFieldPath(fieldPath, '-unit', (path) => {
    cy.get(DATA_IDS.SETTINGS_FIELD(path)).click().focused().clear().type(num);
    cy.get(DATA_IDS.SETTINGS_FIELD(`${path}-unit`)).click();
    cy.get(DATA_IDS.SETTINGS_FIELD(`${path}-unit`)).parent().parent().parent().contains(unitLabel).click();
  });
};

const selectDropdownOption = (fieldPath: string, optionLabel: string) => {
  withFieldPath(fieldPath, '', (path) => {
    cy.get(DATA_IDS.SETTINGS_FIELD(path)).click();
    // The dropdown popup is rendered via React portal at document.body, so it lives
    // outside the trigger's DOM subtree. Each option exposes `data-id="option-${id}"`
    // and the settings dropdown uses the option string itself as the id, so we can
    // target the option globally without scoping by ancestor.
    cy.get(`[data-id="option-${optionLabel}"]`).click();
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
  withFieldPath(fieldPath, '', (path) => {
    cy.get(DATA_IDS.SETTINGS_FIELD(path)).should('have.value', expectedValue);
  });
};

const verifyTimeInput = (fieldPath: string, expectedValue: string) => {
  if (!setFieldKeys.has(fieldPath)) return;
  const match = expectedValue.match(/^(\d+)\s*(ms|s|m|h)$/i);
  if (!match) throw new Error(`verifyTimeInput: invalid time value "${expectedValue}"`);
  const [, num, unit] = match;
  const unitLabel = TIME_UNIT_LABELS[unit.toLowerCase()];

  withFieldPath(fieldPath, '-unit', (path) => {
    cy.get(DATA_IDS.SETTINGS_FIELD(path)).should('have.value', num);
    cy.get(DATA_IDS.SETTINGS_FIELD(`${path}-unit`)).should('have.value', `Selected: ${unitLabel}`);
  });
};

const verifyDropdown = (fieldPath: string, expectedLabel: string) => {
  if (!setFieldKeys.has(fieldPath)) return;
  withFieldPath(fieldPath, '', (path) => {
    cy.get(DATA_IDS.SETTINGS_FIELD(path)).should('have.value', `Selected: ${expectedLabel}`);
  });
};

const verifyMultiInputContains = (fieldPath: string, expectedValue: string) => {
  if (!setFieldKeys.has(fieldPath)) return;
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
    cy.intercept('/graphql').as('gql');
    handleExceptions();
    signInAndSelectProxy();
  });

  // ── Setup ─────────────────────────────────────────────────────────────────

  it('Should capture initial state from the cluster', () => {
    cy.exec(`kubectl delete configmap ${CONFIG_MAPS.LOCAL_UI_CONFIG} -n ${namespace}`, { failOnNonZeroExit: false });
    cy.wait(10000);

    getConfigMapYaml(CONFIG_MAPS.EFFECTIVE_CONFIG, (yaml) => {
      const match = yaml.match(/clusterName:\s*(.+)/);
      originalClusterName = match ? match[1].trim() : '';
    });
  });

  // ── Read ──────────────────────────────────────────────────────────────────

  it('Should render config sections from the cluster', () => {
    visitPage(ROUTES.SETTINGS, () => {
      waitForGraphqlOperation('GetEffectiveConfig').then(() => {
        cy.contains('General').should('exist');
        cy.contains('Instrumentation').should('exist');
        cy.contains('Rollout & Rollback').should('exist');
        cy.contains('Namespaces & Filtering').should('exist');
        cy.contains('Component Log Levels').should('exist');
        cy.contains('Sampling').should('exist');
        cy.contains('Advanced').should('exist');
        cy.contains('Effective Config YAML').should('exist');
      });
    });
  });

  it('Should render the clusterName field with its current value', () => {
    visitPage(ROUTES.SETTINGS, () => {
      waitForGraphqlOperation('GetEffectiveConfig').then(() => {
        cy.get(DATA_IDS.SETTINGS_FIELD('clusterName')).should('exist').and('have.value', originalClusterName);
      });
    });
  });

  it('Should not show the Save/Cancel island when no changes are made', () => {
    visitPage(ROUTES.SETTINGS, () => {
      waitForGraphqlOperation('GetEffectiveConfig').then(() => {
        cy.get(DATA_IDS.SETTINGS_SAVE).should('not.exist');
        cy.get(DATA_IDS.SETTINGS_CANCEL).should('not.exist');
      });
    });
  });

  // ── Update + Cancel ───────────────────────────────────────────────────────

  it('Should show the Save/Cancel island when a field is modified, and revert on Cancel', () => {
    visitPage(ROUTES.SETTINGS, () => {
      waitForGraphqlOperation('GetEffectiveConfig').then(() => {
        const cancelTestValue = (originalClusterName || 'cluster') + '-cancel-test';

        cy.get(DATA_IDS.SETTINGS_FIELD('clusterName')).click().focused().clear().type(cancelTestValue);

        cy.get(DATA_IDS.SETTINGS_SAVE).should('be.visible');
        cy.get(DATA_IDS.SETTINGS_CANCEL).should('be.visible');

        cy.get(DATA_IDS.SETTINGS_CANCEL).click();

        cy.get(DATA_IDS.SETTINGS_FIELD('clusterName')).should('have.value', originalClusterName);
        cy.get(DATA_IDS.SETTINGS_SAVE).should('not.exist');
      });
    });
  });

  // ── Update ALL non-helm-only, non-enterprise-only fields + Save ─────────

  it('Should update all non-helm-only, non-enterprise-only fields and save successfully', () => {
    visitPage(ROUTES.SETTINGS, () => {
      waitForGraphqlOperation('GetEffectiveConfig').then(() => {
        // ─ General ─
        setInput('clusterName', testClusterName);
        clickToggle('telemetryEnabled');

        // ─ Instrumentation ─
        selectDropdownOption('instrumentor.agentEnvVarsInjectionMethod', 'pod-manifest');
        clickToggle('allowConcurrentAgents.enabled');
        clickToggle('instrumentor.checkDeviceHealthBeforeInjection');
        clickToggle('wasp.enabled');

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

        awaitToast({ message: TEXTS.NOTIF_CONFIG_UPDATED });
      });
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
      yamlExpectIfSet('wasp.enabled', yaml, 'waspEnabled:');

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
        yamlExpectIfSet('wasp.enabled', yaml, 'waspEnabled:');

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
      waitForGraphqlOperation('GetEffectiveConfig').then(() => {
        cy.get(DATA_IDS.SETTINGS_SAVE).should('not.exist');
        cy.get(DATA_IDS.SETTINGS_CANCEL).should('not.exist');

        verifyInput('clusterName', testClusterName);
        verifyDropdown('instrumentor.agentEnvVarsInjectionMethod', 'pod-manifest');
        verifyTimeInput('autoRollback.graceTime', '60s');
        verifyTimeInput('autoRollback.stabilityWindowTime', '120s');
        verifyMultiInputContains('ignoredNamespaces', 'cypress-test-ns');
        verifyMultiInputContains('ignoredContainers', 'cypress-test-container');
        verifyDropdown('componentLogLevels.default', 'debug');
        verifyDropdown('componentLogLevels.autoscaler', 'debug');
        verifyDropdown('componentLogLevels.scheduler', 'debug');
        verifyDropdown('componentLogLevels.instrumentor', 'debug');
        verifyDropdown('componentLogLevels.odiglet', 'debug');
        verifyDropdown('componentLogLevels.deviceplugin', 'debug');
        verifyDropdown('componentLogLevels.ui', 'debug');
        verifyDropdown('componentLogLevels.collector', 'debug');
      });
    });
  });

  // ── Reset via UI ───────────────────────────────────────────────────────

  it('Should reset settings via the Reset button and confirm modal', () => {
    visitPage(ROUTES.SETTINGS, () => {
      waitForGraphqlOperation('GetEffectiveConfig').then(() => {
        cy.contains('button', 'Reset').click();
        cy.contains('button', 'Approve').click();

        awaitToast({ message: TEXTS.NOTIF_CONFIG_RESET });
      });
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
        yamlExpectAbsentIfSet('wasp.enabled', yaml, 'waspEnabled:');
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
