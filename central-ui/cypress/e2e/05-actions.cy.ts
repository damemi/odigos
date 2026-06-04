import { CRD_NAMES, DATA_IDS, NAMESPACES, ROUTES, SELECTED_ENTITIES, TEXTS } from '../constants';
import { awaitToast, deleteV2Entity, getCrdById, getCrdIds, handleExceptions, signInAndSelectProxy, updateV2Entity, visitPage, waitForGraphqlOperation } from '../functions';

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

  it(`Should update ${totalEntities} actions via the v2 edit-action-drawer, and notify locally`, () => {
    visitPage(ROUTES.OVERVIEW, () => {
      SELECTED_ENTITIES.ACTIONS.forEach((actionType) => {
        updateV2Entity(
          {
            // actions are fetched in random order, so we locate the row by the type text it shows
            nodeId: 'div',
            nodeContains: actionType,
            prefix: DATA_IDS.ACTION_DRAWER_PREFIX,
            fieldKey: DATA_IDS.ACTION_NAME_INPUT,
            // Embed the action type in the new name. The v2 ListItem renders
            // `name || type`, so renaming every row to a single shared value
            // would erase the per-row text we use to find rows in the delete
            // test (`cy.contains('div', actionType)`). Keeping `actionType` in
            // the value lets that substring lookup keep working.
            fieldValue: `${TEXTS.UPDATED_NAME} ${actionType}`,
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
        // Each action's `actionName` was renamed to `${UPDATED_NAME} ${type}`;
        // verify the shared marker landed on every CRD without coupling to the
        // CRD-id ↔ actionType mapping (which we don't track here).
        getCrdById({ namespace, crdName, crdId, expectedError: '', expectedKey: 'actionName', expectedValue: TEXTS.UPDATED_NAME, expectedValueContains: true });
      });
    });
  });

  it(`Should delete ${totalEntities} actions via the v2 edit-action-drawer, and notify locally`, () => {
    visitPage(ROUTES.OVERVIEW, () => {
      SELECTED_ENTITIES.ACTIONS.forEach((actionType) => {
        deleteV2Entity(
          {
            nodeId: 'div',
            nodeContains: actionType,
            prefix: DATA_IDS.ACTION_DRAWER_PREFIX,
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
