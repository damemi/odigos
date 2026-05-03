import { CRD_NAMES, DATA_IDS, NAMESPACES, ROUTES, SELECTED_ENTITIES, TEXTS } from '../constants';
import { awaitToast, deleteV2Entity, getCrdById, getCrdIds, handleExceptions, signInAndSelectProxy, updateV2Entity, visitPage, waitForGraphqlOperation } from '../functions';

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

        // The "Add Destination" click awaits an async TestConnection mutation
        // before populating `unsavedDestinations` and clearing `activeForm`.
        // Through Central UI → Proxy that round-trip is much slower than in
        // the OSS webapp, so we have to wait for the async work to finish
        // before pressing the save button — otherwise `onSave` runs against
        // an empty `unsavedDestinations` list and no `CreateNewDestination`
        // mutation is ever sent. Two synchronization points:
        //
        //   1. The sticky action row (with `dest-form-add`) is unmounted
        //      once `activeForm` is cleared.
        //   2. The WideDrawer's save button stays disabled until both
        //      `disableSave` and `isFormDirty` settle (briefly disabled
        //      during the activeForm-cleared / unsavedDestinations-populated
        //      transition). Cypress click on a disabled <button> is a silent
        //      no-op, which is exactly what bites us on slower clusters
        //      (1.21.12 / 1.23). `should('not.be.disabled')` retries until
        //      the button is clickable.
        cy.get(DATA_IDS.DEST_FORM_ADD).click();
        cy.get(DATA_IDS.DEST_FORM_ADD).should('not.exist');
        cy.get(DATA_IDS.WIDE_DRAWER_SAVE).should('not.be.disabled').click();

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

  it(`Should update ${totalEntities} destinations via the v2 edit-destination-drawer, and notify locally`, () => {
    visitPage(ROUTES.OVERVIEW, () => {
      updateV2Entity(
        {
          nodeId: DATA_IDS.DESTINATION_NODE(destinationIds[0]),
          nodeContains: SELECTED_ENTITIES.DESTINATION.DISPLAY_NAME,
          prefix: DATA_IDS.DESTINATION_DRAWER_PREFIX,
          fieldKey: DATA_IDS.DESTINATION_NAME_INPUT,
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

  it(`Should delete ${totalEntities} destinations via the v2 edit-destination-drawer, and notify locally`, () => {
    visitPage(ROUTES.OVERVIEW, () => {
      deleteV2Entity(
        {
          nodeId: DATA_IDS.DESTINATION_NODE(destinationIds[0]),
          nodeContains: SELECTED_ENTITIES.DESTINATION.DISPLAY_NAME,
          prefix: DATA_IDS.DESTINATION_DRAWER_PREFIX,
          warnModalTitle: TEXTS.DESTINATION_WARN_MODAL_TITLE,
          warnModalNote: TEXTS.DESTINATION_WARN_MODAL_NOTE,
        },
        () => {
          waitForGraphqlOperation('DeleteDestination').then(() => {
            awaitToast({ message: TEXTS.NOTIF_DESTINATION_DELETED(SELECTED_ENTITIES.DESTINATION.TYPE) }, () => {
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
