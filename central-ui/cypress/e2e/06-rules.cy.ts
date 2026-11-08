import { CRD_NAMES, DATA_IDS, NAMESPACES, ROUTES, SELECTED_ENTITIES, TEXTS } from '../constants';
import { awaitToast, deleteV2Entity, getCrdById, getCrdIds, handleExceptions, interceptGraphql, signInAndSelectProxy, updateV2Entity, visitPage, waitForGraphqlOperation } from '../functions';

const namespace = NAMESPACES.ODIGOS;
const crdName = CRD_NAMES.INSTRUMENTATION_RULE;
const totalEntities = SELECTED_ENTITIES.INSTRUMENTATION_RULES.length;

// Note: standalone webapp tests mock the GetConfig response to force the
// "onprem" tier (so the OSS frontend allows creating rules). Central deals
// only with onprem-licensed proxies, so no mocking is needed here.

describe('Instrumentation Rules CRUD (via Central UI → Proxy)', () => {
  beforeEach(() => {
    handleExceptions();
    signInAndSelectProxy();
    interceptGraphql();
  });

  it(`Should have 0 ${crdName} CRDs in the cluster`, () => {
    getCrdIds({ namespace, crdName, expectedError: TEXTS.NO_RESOURCES(namespace), expectedLength: 0 });
  });

  it(`Should create ${totalEntities} rules via the Central UI, and notify locally`, () => {
    visitPage(ROUTES.OVERVIEW, () => {
      SELECTED_ENTITIES.INSTRUMENTATION_RULES.forEach((ruleType) => {
        cy.get(DATA_IDS.ADD_INSTRUMENTATION_RULE).click();

        cy.get(DATA_IDS.RULE_OPTION(ruleType)).should('exist').click();
        cy.get(DATA_IDS.WIDE_DRAWER_SAVE).click();

        waitForGraphqlOperation('CreateInstrumentationRule').then(() => {
          awaitToast({ message: TEXTS.NOTIF_INSTRUMENTATION_RULE_CREATED(ruleType) });
        });
      });
    });
  });

  it(`Should have ${totalEntities} ${crdName} CRDs in the cluster`, () => {
    getCrdIds({ namespace, crdName, expectedError: '', expectedLength: totalEntities });
  });

  it(`Should update ${totalEntities} rules via the v2 edit-rule-drawer, and notify locally`, () => {
    visitPage(ROUTES.OVERVIEW, () => {
      SELECTED_ENTITIES.INSTRUMENTATION_RULES.forEach((ruleType) => {
        updateV2Entity(
          {
            // rules are fetched in random order, so we locate the row by the type's label it shows
            nodeId: 'div',
            nodeContains: SELECTED_ENTITIES.INSTRUMENTATION_RULE_LABELS[ruleType],
            prefix: DATA_IDS.RULE_DRAWER_PREFIX,
            fieldKey: DATA_IDS.RULE_NAME_INPUT,
            // Embed the rule type in the new name. The v2 ListItem renders
            // `name || type`, so renaming every row to a single shared value
            // would erase the per-row text we use to find rows in the delete
            // test (`cy.contains('div', ruleType)`). Keeping `ruleType` in the
            // value lets that substring lookup keep working.
            fieldValue: `${TEXTS.UPDATED_NAME} ${ruleType}`,
          },
          () => {
            waitForGraphqlOperation('UpdateInstrumentationRule').then(() => {
              awaitToast({ message: TEXTS.NOTIF_INSTRUMENTATION_RULE_UPDATED(ruleType) });
            });
          },
        );
      });
    });
  });

  it(`Should update ${totalEntities} ${crdName} CRDs in the cluster`, () => {
    getCrdIds({ namespace, crdName, expectedError: '', expectedLength: totalEntities }, (crdIds) => {
      crdIds.forEach((crdId) => {
        // Each rule's `ruleName` was renamed to `${UPDATED_NAME} ${type}`;
        // verify the shared marker landed on every CRD without coupling to
        // the CRD-id ↔ ruleType mapping (which we don't track here).
        getCrdById({ namespace, crdName, crdId, expectedError: '', expectedKey: 'ruleName', expectedValue: TEXTS.UPDATED_NAME, expectedValueContains: true });
      });
    });
  });

  it(`Should delete ${totalEntities} rules via the v2 edit-rule-drawer, and notify locally`, () => {
    visitPage(ROUTES.OVERVIEW, () => {
      SELECTED_ENTITIES.INSTRUMENTATION_RULES.forEach((ruleType) => {
        deleteV2Entity(
          {
            nodeId: 'div',
            nodeContains: ruleType,
            prefix: DATA_IDS.RULE_DRAWER_PREFIX,
            warnModalTitle: TEXTS.INSTRUMENTATION_RULE_WARN_MODAL_TITLE,
          },
          () => {
            waitForGraphqlOperation('DeleteInstrumentationRule').then(() => {
              awaitToast({ message: TEXTS.NOTIF_INSTRUMENTATION_RULE_DELETED(ruleType) });
            });
          },
        );
      });
    });
  });

  it(`Should have 0 ${crdName} CRDs in the cluster`, () => {
    getCrdIds({ namespace, crdName, expectedError: TEXTS.NO_RESOURCES(namespace), expectedLength: 0 });
  });
});
