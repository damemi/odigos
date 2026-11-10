import { CRD_NAMES, DATA_IDS, NAMESPACES, ROUTES } from '../constants';
import { awaitToast, cyExec, dismissSamplingOnboardingModal, handleExceptions, interceptGraphql, signInAndSelectProxy, visitPage, waitForGraphqlOperation } from '../functions';

// eslint-disable-next-line @typescript-eslint/no-explicit-any
type CrdSpec = Record<string, any>;

const namespace = NAMESPACES.ODIGOS;
const crdName = CRD_NAMES.SAMPLING;

const NOISY_RULE_NAME = 'Cypress Noisy Rule';
const HIGHLY_RELEVANT_RULE_NAME = 'Cypress Highly Relevant Rule';
const COST_REDUCTION_RULE_NAME = 'Cypress Cost Reduction Rule';
const UPDATED_SUFFIX = ' Updated';

// Segment button labels are "1. Noisy Operations", "2. Highly Relevant Operations",
// "3. Cost Reduction" — substring match is enough for cy.contains.
const TAB_NOISY = 'Noisy Operations';
const TAB_HIGHLY_RELEVANT = 'Highly Relevant Operations';
const TAB_COST_REDUCTION = 'Cost Reduction';

const selectSamplingTab = (tabLabel: string, ruleName?: string) => {
  cy.contains('button', tabLabel).click();
  // `sampling-btn-create-rule` lives in the page header chrome — it stays
  // visible across tab switches and does NOT signal that the Pretty table
  // for this category has remounted / finished MeasurementLayer. Anchor on
  // the rule row (or at least the create button as a last resort).
  if (ruleName) {
    cy.contains(':visible', ruleName, { timeout: 30000 }).should('be.visible');
  } else {
    cy.get(DATA_IDS.SAMPLING_BTN_CREATE_RULE).should('be.visible');
  }
};

// Pretty Table rows wire onClick on the cell wrapper, but MeasurementLayer
// column measurement can swallow the first click (onRuleClick never runs →
// view drawer never mounts → sampling-view-btn-edit missing). Seen on Noisy
// first, then on Cost Reduction after the same "wait for create button"
// false settle. Queue a few conditional re-clicks; Cypress runs each
// `.then` after the previous click has been applied to the DOM.
const openSamplingRuleView = (tabLabel: string, ruleName: string) => {
  selectSamplingTab(tabLabel, ruleName);

  for (let attempt = 0; attempt < 6; attempt++) {
    cy.get('body').then(($body) => {
      if ($body.find(DATA_IDS.SAMPLING_VIEW_BTN_EDIT).length > 0) {
        return;
      }
      // Re-resolve the row each attempt — do not `.should('be.visible').click()`
      // which settles the subject and misses Pretty table remounts.
      cy.contains(':visible', ruleName, { timeout: 30000 }).click();
    });
  }

  cy.get(DATA_IDS.SAMPLING_VIEW_BTN_EDIT, { timeout: 15000 }).should('be.visible');
};

// Open a sampling rule's view drawer, then enter edit mode.
const openSamplingRuleEditor = (tabLabel: string, ruleName: string) => {
  openSamplingRuleView(tabLabel, ruleName);
  cy.get(DATA_IDS.SAMPLING_VIEW_BTN_EDIT, { timeout: 15000 }).should('be.visible').click();
};

const openSamplingRuleForDelete = (tabLabel: string, ruleName: string) => {
  openSamplingRuleView(tabLabel, ruleName);
  cy.get(DATA_IDS.SAMPLING_VIEW_BTN_DELETE, { timeout: 15000 }).should('be.visible').click();
  cy.contains('button', 'Delete').click();
};

describe('Sampling Rules CRUD (via Central UI → Proxy)', () => {
  beforeEach(() => {
    handleExceptions();
    signInAndSelectProxy();
    interceptGraphql();
  });

  // ── Cleanup ──────────────────────────────────────────────────────────────

  it('Should clean up any existing sampling CRDs before tests', () => {
    cyExec(`kubectl delete ${crdName} --all -n ${namespace}`, { failOnNonZeroExit: false });
    cy.wait(2000);
  });

  // ── CREATE: Noisy Operation Rule ─────────────────────────────────────────

  it('Should create a Noisy Operation rule with HTTP server route', () => {
    visitPage(ROUTES.SAMPLING, () => {
      waitForGraphqlOperation('GetSamplingRules').then(() => {
        // First visit after CRD cleanup — the ui-kit onboarding modal opens
        // automatically when there are no sampling rules. Skip past it so
        // we can interact with the page below.
        dismissSamplingOnboardingModal();

        cy.get(DATA_IDS.SAMPLING_BTN_CREATE_RULE).click();

        cy.get('input[name=sampling-input-rule-name]').type(NOISY_RULE_NAME);
        cy.get('input[name=sampling-input-note]').type('Created by Cypress');

        cy.contains('button', 'HTTP Server').scrollIntoView().click({ force: true });

        cy.get('input[name=sampling-input-route]').type('api/health');

        cy.contains('button', 'Sample').scrollIntoView().click({ force: true });
        cy.contains('button', '1%').scrollIntoView().click({ force: true });

        cy.get(DATA_IDS.SAMPLING_CREATE_BTN_SUBMIT).click();

        waitForGraphqlOperation('CreateNoisyOperationRule').then(() => {
          awaitToast({ message: 'Successfully created' });
        });
      });
    });
  });

  // ── CREATE: Highly Relevant Operation Rule ───────────────────────────────

  it('Should create a Highly Relevant Operation rule (Duration type)', () => {
    visitPage(ROUTES.SAMPLING, () => {
      waitForGraphqlOperation('GetSamplingRules').then(() => {
        selectSamplingTab(TAB_HIGHLY_RELEVANT);

        cy.get(DATA_IDS.SAMPLING_BTN_CREATE_RULE).click();

        cy.get('input[name=sampling-input-rule-name]').type(HIGHLY_RELEVANT_RULE_NAME);
        cy.get('input[name=sampling-input-note]').type('Created by Cypress');

        // Duration: an Error rule on the whole cluster would read as the "Keep All Error Traces" preset, which isn't listed.
        cy.get('[data-id=sampling-segment-rule-type-Duration]').scrollIntoView().click({ force: true });

        cy.get(DATA_IDS.SAMPLING_CREATE_BTN_SUBMIT).click();

        waitForGraphqlOperation('CreateHighlyRelevantOperationRule').then(() => {
          awaitToast({ message: 'Successfully created' });
        });
      });
    });
  });

  // ── CREATE: Cost Reduction Rule ──────────────────────────────────────────

  it('Should create a Cost Reduction rule', () => {
    visitPage(ROUTES.SAMPLING, () => {
      waitForGraphqlOperation('GetSamplingRules').then(() => {
        selectSamplingTab(TAB_COST_REDUCTION);

        cy.get(DATA_IDS.SAMPLING_BTN_CREATE_RULE).click();

        cy.get('input[name=sampling-input-rule-name]').type(COST_REDUCTION_RULE_NAME);
        cy.get('input[name=sampling-input-note]').type('Created by Cypress');

        cy.contains('button', 'HTTP Server').scrollIntoView().click({ force: true });
        cy.get('input[name=sampling-input-route]').type('api/checkout');

        cy.contains('button', '25%').scrollIntoView().click({ force: true });

        cy.get(DATA_IDS.SAMPLING_CREATE_BTN_SUBMIT).click();

        waitForGraphqlOperation('CreateCostReductionRule').then(() => {
          awaitToast({ message: 'Successfully created' });
        });
      });
    });
  });

  // ── VERIFY CREATE via kubectl ────────────────────────────────────────────

  it('Should have sampling CRDs in the cluster after creation', () => {
    cyExec(`kubectl get ${crdName} -n ${namespace} -o json`).then(({ stdout }) => {
      const parsed = JSON.parse(stdout);
      const items = parsed.items || [];
      expect(items.length).to.be.greaterThan(0);

      const spec: CrdSpec = items[0].spec;

      const noisyRule = (spec.noisyOperations || []).find((r: CrdSpec) => r.name === NOISY_RULE_NAME);
      expect(noisyRule, `Expected noisy rule "${NOISY_RULE_NAME}" to exist`).to.not.be.undefined;
      expect(noisyRule.operation?.httpServer?.route).to.eq('api/health');

      const hrRule = (spec.highlyRelevantOperations || []).find((r: CrdSpec) => r.name === HIGHLY_RELEVANT_RULE_NAME);
      expect(hrRule, `Expected highly relevant rule "${HIGHLY_RELEVANT_RULE_NAME}" to exist`).to.not.be.undefined;

      const crRule = (spec.costReductionRules || []).find((r: CrdSpec) => r.name === COST_REDUCTION_RULE_NAME);
      expect(crRule, `Expected cost reduction rule "${COST_REDUCTION_RULE_NAME}" to exist`).to.not.be.undefined;
    });
  });

  // ── READ: Verify rules appear in the UI ──────────────────────────────────

  it('Should display the Noisy Operation rule in the UI', () => {
    visitPage(ROUTES.SAMPLING, () => {
      waitForGraphqlOperation('GetSamplingRules').then(() => {
        selectSamplingTab(TAB_NOISY, NOISY_RULE_NAME);
      });
    });
  });

  it('Should display the Highly Relevant Operation rule in the UI', () => {
    visitPage(ROUTES.SAMPLING, () => {
      waitForGraphqlOperation('GetSamplingRules').then(() => {
        selectSamplingTab(TAB_HIGHLY_RELEVANT, HIGHLY_RELEVANT_RULE_NAME);
      });
    });
  });

  it('Should display the Cost Reduction rule in the UI', () => {
    visitPage(ROUTES.SAMPLING, () => {
      waitForGraphqlOperation('GetSamplingRules').then(() => {
        selectSamplingTab(TAB_COST_REDUCTION, COST_REDUCTION_RULE_NAME);
      });
    });
  });

  // ── UPDATE: Edit the Noisy Operation rule ────────────────────────────────

  it('Should update the Noisy Operation rule name', () => {
    visitPage(ROUTES.SAMPLING, () => {
      waitForGraphqlOperation('GetSamplingRules').then(() => {
        openSamplingRuleEditor(TAB_NOISY, NOISY_RULE_NAME);

        cy.get('input[name=sampling-input-rule-name]').click().focused().clear().type(NOISY_RULE_NAME + UPDATED_SUFFIX);

        cy.get(DATA_IDS.SAMPLING_VIEW_EDIT_BTN_SAVE).click();

        waitForGraphqlOperation('UpdateNoisyOperationRule').then(() => {
          awaitToast({ message: 'Successfully updated' });
        });
      });
    });
  });

  // ── UPDATE: Edit the Highly Relevant rule ────────────────────────────────

  it('Should update the Highly Relevant Operation rule name', () => {
    visitPage(ROUTES.SAMPLING, () => {
      waitForGraphqlOperation('GetSamplingRules').then(() => {
        openSamplingRuleEditor(TAB_HIGHLY_RELEVANT, HIGHLY_RELEVANT_RULE_NAME);

        cy.get('input[name=sampling-input-rule-name]')
          .click()
          .focused()
          .clear()
          .type(HIGHLY_RELEVANT_RULE_NAME + UPDATED_SUFFIX);

        cy.get(DATA_IDS.SAMPLING_VIEW_EDIT_BTN_SAVE).click();

        waitForGraphqlOperation('UpdateHighlyRelevantOperationRule').then(() => {
          awaitToast({ message: 'Successfully updated' });
        });
      });
    });
  });

  // ── UPDATE: Edit the Cost Reduction rule ─────────────────────────────────

  it('Should update the Cost Reduction rule name', () => {
    visitPage(ROUTES.SAMPLING, () => {
      waitForGraphqlOperation('GetSamplingRules').then(() => {
        openSamplingRuleEditor(TAB_COST_REDUCTION, COST_REDUCTION_RULE_NAME);

        cy.get('input[name=sampling-input-rule-name]')
          .click()
          .focused()
          .clear()
          .type(COST_REDUCTION_RULE_NAME + UPDATED_SUFFIX);

        cy.get(DATA_IDS.SAMPLING_VIEW_EDIT_BTN_SAVE).click();

        waitForGraphqlOperation('UpdateCostReductionRule').then(() => {
          awaitToast({ message: 'Successfully updated' });
        });
      });
    });
  });

  // ── VERIFY UPDATE via kubectl ────────────────────────────────────────────

  it('Should have updated rule names in the cluster', () => {
    cyExec(`kubectl get ${crdName} -n ${namespace} -o json`).then(({ stdout }) => {
      const parsed = JSON.parse(stdout);
      const items = parsed.items || [];
      expect(items.length).to.be.greaterThan(0);

      const spec: CrdSpec = items[0].spec;

      const noisyRule = (spec.noisyOperations || []).find((r: CrdSpec) => r.name === NOISY_RULE_NAME + UPDATED_SUFFIX);
      expect(noisyRule, `Expected updated noisy rule name`).to.not.be.undefined;

      const hrRule = (spec.highlyRelevantOperations || []).find((r: CrdSpec) => r.name === HIGHLY_RELEVANT_RULE_NAME + UPDATED_SUFFIX);
      expect(hrRule, `Expected updated highly relevant rule name`).to.not.be.undefined;

      const crRule = (spec.costReductionRules || []).find((r: CrdSpec) => r.name === COST_REDUCTION_RULE_NAME + UPDATED_SUFFIX);
      expect(crRule, `Expected updated cost reduction rule name`).to.not.be.undefined;
    });
  });

  // ── VERIFY UPDATE in UI ──────────────────────────────────────────────────

  it('Should display updated Noisy rule name in the UI', () => {
    visitPage(ROUTES.SAMPLING, () => {
      waitForGraphqlOperation('GetSamplingRules').then(() => {
        selectSamplingTab(TAB_NOISY);
        cy.contains(NOISY_RULE_NAME + UPDATED_SUFFIX).should('exist');
      });
    });
  });

  // ── DELETE: Delete the Noisy Operation rule ──────────────────────────────

  it('Should delete the Noisy Operation rule via the view drawer', () => {
    visitPage(ROUTES.SAMPLING, () => {
      waitForGraphqlOperation('GetSamplingRules').then(() => {
        openSamplingRuleForDelete(TAB_NOISY, NOISY_RULE_NAME + UPDATED_SUFFIX);

        waitForGraphqlOperation('DeleteNoisyOperationRule').then(() => {
          awaitToast({ message: 'Successfully deleted' });
        });
      });
    });
  });

  // ── DELETE: Delete the Highly Relevant rule via view drawer ────────────────

  it('Should delete the Highly Relevant Operation rule via the view drawer', () => {
    visitPage(ROUTES.SAMPLING, () => {
      waitForGraphqlOperation('GetSamplingRules').then(() => {
        openSamplingRuleForDelete(TAB_HIGHLY_RELEVANT, HIGHLY_RELEVANT_RULE_NAME + UPDATED_SUFFIX);

        waitForGraphqlOperation('DeleteHighlyRelevantOperationRule').then(() => {
          awaitToast({ message: 'Successfully deleted' });
        });
      });
    });
  });

  // ── DELETE: Delete the Cost Reduction rule ───────────────────────────────

  it('Should delete the Cost Reduction rule via the view drawer', () => {
    visitPage(ROUTES.SAMPLING, () => {
      waitForGraphqlOperation('GetSamplingRules').then(() => {
        openSamplingRuleForDelete(TAB_COST_REDUCTION, COST_REDUCTION_RULE_NAME + UPDATED_SUFFIX);

        waitForGraphqlOperation('DeleteCostReductionRule').then(() => {
          awaitToast({ message: 'Successfully deleted' });
        });
      });
    });
  });

  // ── VERIFY DELETE via kubectl ────────────────────────────────────────────

  it('Should have no user-created sampling rules in the cluster after deletion', () => {
    cyExec(`kubectl get ${crdName} -n ${namespace} -o json`, { failOnNonZeroExit: false }).then(({ stdout, stderr }) => {
      if (stderr.includes('No resources found') || !stdout.trim()) {
        return;
      }

      const parsed = JSON.parse(stdout);
      const items = parsed.items || [];

      if (items.length === 0) return;

      const spec: CrdSpec = items[0].spec;
      const noisyRules = spec.noisyOperations || [];
      const hrRules = spec.highlyRelevantOperations || [];
      const crRules = spec.costReductionRules || [];

      expect(noisyRules.find((r: CrdSpec) => r.name?.includes('Cypress'))).to.be.undefined;
      expect(hrRules.find((r: CrdSpec) => r.name?.includes('Cypress'))).to.be.undefined;
      expect(crRules.find((r: CrdSpec) => r.name?.includes('Cypress'))).to.be.undefined;
    });
  });

  // ── VERIFY DELETE in UI ──────────────────────────────────────────────────

  it('Should not display any Cypress test rules in the UI after deletion', () => {
    visitPage(ROUTES.SAMPLING, () => {
      waitForGraphqlOperation('GetSamplingRules').then(() => {
        selectSamplingTab(TAB_NOISY);
        cy.contains(NOISY_RULE_NAME).should('not.exist');
        cy.contains(NOISY_RULE_NAME + UPDATED_SUFFIX).should('not.exist');

        selectSamplingTab(TAB_HIGHLY_RELEVANT);
        cy.contains(HIGHLY_RELEVANT_RULE_NAME).should('not.exist');
        cy.contains(HIGHLY_RELEVANT_RULE_NAME + UPDATED_SUFFIX).should('not.exist');

        selectSamplingTab(TAB_COST_REDUCTION);
        cy.contains(COST_REDUCTION_RULE_NAME).should('not.exist');
        cy.contains(COST_REDUCTION_RULE_NAME + UPDATED_SUFFIX).should('not.exist');
      });
    });
  });
});
