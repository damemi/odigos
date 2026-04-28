import { CRD_NAMES, DATA_IDS, NAMESPACES, ROUTES, SELECTED_ENTITIES, TEXTS } from '../constants';
import { awaitToast, deleteEntity, getCrdById, getCrdIds, handleExceptions, signInAndSelectProxy, updateEntity, visitPage, waitForGraphqlOperation } from '../functions';

const namespace = NAMESPACES.ODIGOS;
const crdName = CRD_NAMES.ACTION;
const totalEntities = SELECTED_ENTITIES.ACTIONS.length;

describe('Actions CRUD (via Central UI → Proxy)', () => {
  beforeEach(() => {
    cy.intercept('/graphql').as('gql');
    handleExceptions();
    signInAndSelectProxy();
  });

  it(`Should have 0 ${crdName} CRDs in the cluster`, () => {
    getCrdIds({ namespace, crdName, expectedError: TEXTS.NO_RESOURCES(namespace), expectedLength: 0 });
  });

  it(`Should create ${totalEntities} actions via the Central UI, and notify locally`, () => {
    visitPage(ROUTES.OVERVIEW, () => {
      SELECTED_ENTITIES.ACTIONS.forEach((actionType) => {
        cy.get(DATA_IDS.ADD_ACTION).click();

        cy.get(DATA_IDS.ACTION_OPTION(actionType)).should('exist').click();

        switch (actionType) {
          case 'K8sAttributesResolver': {
            break;
          }
          case 'AddClusterInfo': {
            cy.get('[data-id=clusterAttributes]').find('input[placeholder="Attribute name"]').type('key');
            cy.get('[data-id=clusterAttributes]').find('input[placeholder="Attribute value"]').type('val');
            break;
          }
          case 'DeleteAttribute': {
            cy.get('[data-id=attributeNamesToDelete]').find('input').type('test');
            break;
          }
          case 'RenameAttribute': {
            cy.get('[data-id=renames]').find('input[placeholder="Old key"]').type('1');
            cy.get('[data-id=renames]').find('input[placeholder="New key"]').type('one');
            break;
          }
          case 'PiiMasking': {
            break;
          }
          case 'ErrorSampler': {
            cy.get('input[data-id=fallbackSamplingRatio]').type('1');
            break;
          }
          case 'ProbabilisticSampler': {
            cy.get('input[data-id=samplingPercentage]').type('1');
            break;
          }
          case 'LatencySampler': {
            cy.get('tbody').find('input[placeholder="e.g. my-service"]').type('service');
            cy.get('tbody').find('input[placeholder="e.g. /api/v1/users"]').type('/path');
            cy.get('tbody').find('input[placeholder="e.g. 1000"]').type('1');
            cy.get('tbody').find('input[placeholder="e.g. 100"]').type('1');
            break;
          }
          case 'ServiceNameSampler': {
            cy.get('tbody').find('input[placeholder="e.g. my-service"]').type('service');
            cy.get('tbody').find('input[placeholder="e.g. 10"]').type('1');
            cy.get('tbody').find('input[placeholder="e.g. 100"]').type('1');
            break;
          }
          case 'SpanAttributeSampler': {
            cy.get('[data-id=attributeFilters]').find('input[data-id=serviceName]').type('service');
            cy.get('[data-id=attributeFilters]').find('input[data-id=attributeKey]').type('attribute');
            cy.get('[data-id=attributeFilters]').find('input[data-id=fallbackSamplingRatio]').first().type('1');

            cy.get('[data-id=attributeFilters]').find('input[data-id=condition]').scrollIntoView().click({ force: true });
            cy.get('[data-id=option-stringCondition]').click({ force: true });

            cy.get('[data-id=attributeFilters]').find('input[data-id=operation]').scrollIntoView().click({ force: true });
            cy.get('[data-id=option-equals]').click({ force: true });

            cy.get('[data-id=attributeFilters]').find('input[data-id=expectedValue]').type('x');
            break;
          }

          default: {
            cy.get('unknown action').should('eq', true);
            break;
          }
        }

        cy.get(DATA_IDS.WIDE_DRAWER_SAVE).click();

        waitForGraphqlOperation('CreateAction').then(() => {
          awaitToast({ message: TEXTS.NOTIF_ACTION_CREATED(actionType) });
        });
      });
    });
  });

  it(`Should have ${totalEntities} ${crdName} CRDs in the cluster`, () => {
    getCrdIds({ namespace, crdName, expectedError: '', expectedLength: totalEntities });
  });

  it(`Should update ${totalEntities} actions via the Central UI, and notify locally`, () => {
    visitPage(ROUTES.OVERVIEW, () => {
      SELECTED_ENTITIES.ACTIONS.forEach((actionType) => {
        updateEntity(
          {
            nodeId: 'div',
            nodeContains: actionType,
            fieldKey: DATA_IDS.TITLE,
            fieldValue: TEXTS.UPDATED_NAME,
          },
          () => {
            waitForGraphqlOperation('UpdateAction').then(() => {
              awaitToast({ message: TEXTS.NOTIF_ACTION_UPDATED(actionType) });
            });
          },
        );
      });
    });
  });

  it(`Should update ${totalEntities} ${crdName} CRDs in the cluster`, () => {
    getCrdIds({ namespace, crdName, expectedError: '', expectedLength: totalEntities }, (crdIds) => {
      crdIds.forEach((crdId) => {
        getCrdById({ namespace, crdName, crdId, expectedError: '', expectedKey: 'actionName', expectedValue: TEXTS.UPDATED_NAME });
      });
    });
  });

  it(`Should delete ${totalEntities} actions via the Central UI, and notify locally`, () => {
    visitPage(ROUTES.OVERVIEW, () => {
      SELECTED_ENTITIES.ACTIONS.forEach((actionType) => {
        deleteEntity(
          {
            nodeId: 'div',
            nodeContains: actionType,
            warnModalTitle: TEXTS.ACTION_WARN_MODAL_TITLE,
          },
          () => {
            waitForGraphqlOperation('DeleteAction').then(() => {
              awaitToast({ message: TEXTS.NOTIF_ACTION_DELETED(actionType) });
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
