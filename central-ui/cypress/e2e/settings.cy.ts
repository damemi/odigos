import { CONFIG_MAPS, DATA_IDS, NAMESPACES, ROUTES, TEXTS } from '../constants';
import { awaitToast, handleExceptions, signInAndSelectProxy, visitPage, waitForGraphqlOperation } from '../functions';

const namespace = NAMESPACES.ODIGOS;
const testClusterName = 'cypress-e2e-test';
let originalClusterName = '';

const getConfigMapYaml = (configMapName: string, callback: (yaml: string) => void) => {
  cy.exec(`kubectl get configmap ${configMapName} -n ${namespace} -o jsonpath='{.data.config\\.yaml}'`).then(({ stdout }) => {
    callback(stdout);
  });
};

const clickToggle = (fieldPath: string) => {
  cy.get(DATA_IDS.SETTINGS_FIELD(fieldPath)).children().first().click();
};

const setInput = (fieldPath: string, value: string) => {
  cy.get(DATA_IDS.SETTINGS_FIELD(fieldPath)).click().focused().clear().type(value);
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

  cy.get(DATA_IDS.SETTINGS_FIELD(fieldPath)).click().focused().clear().type(num);
  cy.get(DATA_IDS.SETTINGS_FIELD(`${fieldPath}-unit`)).click();
  cy.get(DATA_IDS.SETTINGS_FIELD(`${fieldPath}-unit`))
    .parent()
    .parent()
    .parent()
    .contains(unitLabel)
    .click();
};

const selectDropdownOption = (fieldPath: string, optionLabel: string) => {
  cy.get(DATA_IDS.SETTINGS_FIELD(fieldPath)).click();
  cy.get(DATA_IDS.SETTINGS_FIELD(fieldPath)).parent().parent().parent().contains(optionLabel).click();
};

const addMultiInputValue = (fieldPath: string, value: string) => {
  cy.get(DATA_IDS.SETTINGS_FIELD(fieldPath)).find('button').first().click();
  cy.get(DATA_IDS.SETTINGS_FIELD(fieldPath)).find('input').last().clear().type(value);
};

const verifyInput = (fieldPath: string, expectedValue: string) => {
  cy.get(DATA_IDS.SETTINGS_FIELD(fieldPath)).should('have.value', expectedValue);
};

const verifyTimeInput = (fieldPath: string, expectedValue: string) => {
  const match = expectedValue.match(/^(\d+)\s*(ms|s|m|h)$/i);
  if (!match) throw new Error(`verifyTimeInput: invalid time value "${expectedValue}"`);
  const [, num, unit] = match;
  const unitLabel = TIME_UNIT_LABELS[unit.toLowerCase()];

  cy.get(DATA_IDS.SETTINGS_FIELD(fieldPath)).should('have.value', num);
  cy.get(DATA_IDS.SETTINGS_FIELD(`${fieldPath}-unit`)).should('have.value', `Selected: ${unitLabel}`);
};

const verifyDropdown = (fieldPath: string, expectedLabel: string) => {
  cy.get(DATA_IDS.SETTINGS_FIELD(fieldPath)).should('have.value', `Selected: ${expectedLabel}`);
};

const verifyMultiInputContains = (fieldPath: string, expectedValue: string) => {
  cy.get(DATA_IDS.SETTINGS_FIELD(fieldPath))
    .find('input')
    .should(($inputs) => {
      const values = $inputs.toArray().map((el) => (el as HTMLInputElement).value);
      expect(values).to.include(expectedValue);
    });
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
      expect(yaml).to.contain(`clusterName: ${testClusterName}`);

      expect(yaml).to.contain('agentEnvVarsInjectionMethod: pod-manifest');
      expect(yaml).to.contain('allowConcurrentAgents:');
      expect(yaml).to.contain('checkDeviceHealthBeforeInjection:');
      expect(yaml).to.contain('waspEnabled:');

      expect(yaml).to.contain('automaticRolloutDisabled:');
      expect(yaml).to.contain('maxConcurrentRollouts: 5');
      expect(yaml).to.contain('rollbackDisabled:');
      expect(yaml).to.satisfy((s: string) => s.includes('rollbackGraceTime: 60s') || s.includes('rollbackGraceTime: "60s"'));
      expect(yaml).to.satisfy((s: string) => s.includes('rollbackStabilityWindow: 120s') || s.includes('rollbackStabilityWindow: "120s"'));

      expect(yaml).to.contain('cypress-test-ns');
      expect(yaml).to.contain('cypress-test-container');
      expect(yaml).to.contain('ignoreOdigosNamespace:');

      expect(yaml).to.contain('componentLogLevels:');

      expect(yaml).to.contain('sampling:');
      expect(yaml).to.contain('dryRun:');
      expect(yaml).to.contain('spanSamplingAttributes:');
      expect(yaml).to.contain('tailSampling:');
      expect(yaml).to.satisfy((s: string) => s.includes('traceAggregationWaitDuration: 45s') || s.includes('traceAggregationWaitDuration: "45s"'));
      expect(yaml).to.contain('k8sHealthProbesSampling:');
      expect(yaml).to.contain('keepPercentage: 50');
    });
  });

  it(`Should have reconciled changes into the ${CONFIG_MAPS.EFFECTIVE_CONFIG} ConfigMap`, () => {
    cy.wait(5000).then(() => {
      getConfigMapYaml(CONFIG_MAPS.EFFECTIVE_CONFIG, (yaml) => {
        expect(yaml).to.contain(`clusterName: ${testClusterName}`);

        expect(yaml).to.contain('agentEnvVarsInjectionMethod: pod-manifest');
        expect(yaml).to.contain('allowConcurrentAgents:');
        expect(yaml).to.contain('checkDeviceHealthBeforeInjection:');
        expect(yaml).to.contain('waspEnabled:');

        expect(yaml).to.contain('automaticRolloutDisabled:');
        expect(yaml).to.contain('maxConcurrentRollouts: 5');
        expect(yaml).to.contain('rollbackDisabled:');
        expect(yaml).to.satisfy((s: string) => s.includes('rollbackGraceTime: 60s') || s.includes('rollbackGraceTime: "60s"'));
        expect(yaml).to.satisfy((s: string) => s.includes('rollbackStabilityWindow: 120s') || s.includes('rollbackStabilityWindow: "120s"'));

        expect(yaml).to.contain('cypress-test-ns');
        expect(yaml).to.contain('cypress-test-container');

        expect(yaml).to.satisfy((s: string) => s.includes('traceAggregationWaitDuration: 45s') || s.includes('traceAggregationWaitDuration: "45s"'));
        expect(yaml).to.contain('keepPercentage: 50');
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
        expect(yaml).to.not.contain(`clusterName: ${testClusterName}`);
        expect(yaml).to.not.contain('agentEnvVarsInjectionMethod: pod-manifest');
        expect(yaml).to.not.contain('cypress-test-ns');
        expect(yaml).to.not.contain('cypress-test-container');
        expect(yaml).to.not.contain('maxConcurrentRollouts: 5');
        expect(yaml).to.not.contain('keepPercentage: 50');
        expect(yaml).to.not.contain('waspEnabled:');
        expect(yaml).to.not.contain('allowConcurrentAgents:');
      });
    });
  });

  it(`Should have reconciled the reset into the ${CONFIG_MAPS.EFFECTIVE_CONFIG} ConfigMap`, () => {
    cy.wait(5000).then(() => {
      getConfigMapYaml(CONFIG_MAPS.EFFECTIVE_CONFIG, (yaml) => {
        expect(yaml).to.not.contain(`clusterName: ${testClusterName}`);
        expect(yaml).to.not.contain('agentEnvVarsInjectionMethod: pod-manifest');
        expect(yaml).to.not.contain('cypress-test-ns');
        expect(yaml).to.not.contain('cypress-test-container');
        expect(yaml).to.not.contain('maxConcurrentRollouts: 5');
        expect(yaml).to.not.contain('keepPercentage: 50');
      });
    });
  });
});
