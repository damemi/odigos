import { CRD_NAMES, DATA_IDS, NAMESPACES, ROUTES, SELECTED_ENTITIES, TEXTS } from '../constants';
import { awaitToast, deleteEntity, getCrdById, getCrdIds, handleExceptions, signInAndSelectProxy, updateEntity, visitPage, waitForGraphqlOperation } from '../functions';

const namespace = NAMESPACES.ODIGOS;
const crdName = CRD_NAMES.DESTINATION;
const totalEntities = 1;
const destinationIds: string[] = [];

describe('Destinations CRUD (via Central UI → Proxy)', () => {
  beforeEach(() => {
    cy.intercept('/graphql').as('gql');
    handleExceptions();
    signInAndSelectProxy();
  });

  it(`Should have 0 ${crdName} CRDs in the cluster`, () => {
    getCrdIds({ namespace, crdName, expectedError: TEXTS.NO_RESOURCES(namespace), expectedLength: 0 });
  });

  it(`Should create ${totalEntities} destinations via the Central UI, and notify locally`, () => {
    visitPage(ROUTES.OVERVIEW, () => {
      cy.get(DATA_IDS.ADD_DESTINATION).click();

      // Wait for detected destinations to load before selecting
      waitForGraphqlOperation('GetPotentialDestinations').then(() => {
        cy.contains('Detected by system').should('be.visible');
        cy.get(DATA_IDS.SELECT_DESTINATION).first().should('exist').click({ force: true });
        cy.get(DATA_IDS.SELECT_DESTINATION_AUTOFILL_FIELD).should('have.value', SELECTED_ENTITIES.DESTINATION.AUTOFILL_VALUE);

        cy.get(DATA_IDS.DEST_FORM_ADD).click();
        cy.get(DATA_IDS.WIDE_DRAWER_SAVE).click();

        waitForGraphqlOperation('CreateNewDestination').then(() => {
          cy.get(DATA_IDS.WIDE_DRAWER_SAVE).should('not.exist');
          cy.wait(2000);
        });
      });
    });
  });

  it(`Should have ${totalEntities} ${crdName} CRDs in the cluster`, () => {
    getCrdIds({ namespace, crdName, expectedError: '', expectedLength: totalEntities }, (crdIds) => {
      crdIds.forEach((crdId) => {
        destinationIds.push(crdId);
      });
    });
  });

  it(`Should update ${totalEntities} destinations via the Central UI, and notify locally`, () => {
    visitPage(ROUTES.OVERVIEW, () => {
      updateEntity(
        {
          nodeId: DATA_IDS.DESTINATION_NODE(destinationIds[0]),
          nodeContains: SELECTED_ENTITIES.DESTINATION.DISPLAY_NAME,
          fieldKey: DATA_IDS.TITLE,
          fieldValue: TEXTS.UPDATED_NAME,
        },
        () => {
          waitForGraphqlOperation('UpdateDestination').then(() => {
            awaitToast({ message: TEXTS.NOTIF_DESTINATION_UPDATED(SELECTED_ENTITIES.DESTINATION.TYPE) });
          });
        },
      );
    });
  });

  it(`Should update ${totalEntities} ${crdName} CRDs in the cluster`, () => {
    getCrdIds({ namespace, crdName, expectedError: '', expectedLength: totalEntities }, (crdIds) => {
      crdIds.forEach((crdId) => {
        getCrdById({ namespace, crdName, crdId, expectedError: '', expectedKey: 'destinationName', expectedValue: TEXTS.UPDATED_NAME });
      });
    });
  });

  it(`Should delete ${totalEntities} destinations via the Central UI, and notify locally`, () => {
    visitPage(ROUTES.OVERVIEW, () => {
      deleteEntity(
        {
          nodeId: DATA_IDS.DESTINATION_NODE(destinationIds[0]),
          nodeContains: SELECTED_ENTITIES.DESTINATION.DISPLAY_NAME,
          warnModalTitle: TEXTS.DESTINATION_WARN_MODAL_TITLE,
          warnModalNote: TEXTS.DESTINATION_WARN_MODAL_NOTE,
        },
        () => {
          waitForGraphqlOperation('DeleteDestination').then(() => {
            awaitToast({ message: TEXTS.NOTIF_DESTINATION_DELETED(totalEntities) }, () => {
              getCrdIds({ namespace, crdName, expectedError: TEXTS.NO_RESOURCES(namespace), expectedLength: 0 });
            });
          });
        },
      );
    });
  });

  it(`Should have 0 ${crdName} CRDs in the cluster`, () => {
    getCrdIds({ namespace, crdName, expectedError: TEXTS.NO_RESOURCES(namespace), expectedLength: 0 });
  });
});
