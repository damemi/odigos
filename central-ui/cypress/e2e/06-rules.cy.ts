import { CRD_NAMES, DATA_IDS, NAMESPACES, ROUTES, SELECTED_ENTITIES, TEXTS } from '../constants';
import { awaitToast, deleteEntity, getCrdById, getCrdIds, handleExceptions, signInAndSelectProxy, updateEntity, visitPage, waitForGraphqlOperation } from '../functions';

const namespace = NAMESPACES.ODIGOS;
const crdName = CRD_NAMES.INSTRUMENTATION_RULE;
const totalEntities = SELECTED_ENTITIES.INSTRUMENTATION_RULES.length;

// Note: standalone webapp tests mock the GetConfig response to force the
// "onprem" tier (so the OSS frontend allows creating rules). Central deals
// only with onprem-licensed proxies, so no mocking is needed here.

describe('Instrumentation Rules CRUD (via Central UI → Proxy)', () => {
  beforeEach(() => {
    cy.intercept('/graphql').as('gql');
    handleExceptions();
    signInAndSelectProxy();
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

  it(`Should update ${totalEntities} rules via the Central UI, and notify locally`, () => {
    visitPage(ROUTES.OVERVIEW, () => {
      SELECTED_ENTITIES.INSTRUMENTATION_RULES.forEach((ruleType) => {
        updateEntity(
          {
            nodeId: 'div',
            nodeContains: ruleType,
            fieldKey: DATA_IDS.TITLE,
            fieldValue: TEXTS.UPDATED_NAME,
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
        getCrdById({ namespace, crdName, crdId, expectedError: '', expectedKey: 'ruleName', expectedValue: TEXTS.UPDATED_NAME });
      });
    });
  });

  it(`Should delete ${totalEntities} rules via the Central UI, and notify locally`, () => {
    visitPage(ROUTES.OVERVIEW, () => {
      SELECTED_ENTITIES.INSTRUMENTATION_RULES.forEach((ruleType) => {
        deleteEntity(
          {
            nodeId: 'div',
            nodeContains: ruleType,
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
