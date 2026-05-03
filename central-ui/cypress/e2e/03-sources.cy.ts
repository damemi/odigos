import { CRD_NAMES, DATA_IDS, NAMESPACES, ROUTES, SELECTED_ENTITIES, TEXTS } from '../constants';
import { awaitToast, findCrdId, getCrdById, getCrdIds, handleExceptions, signInAndSelectProxy, updateEntity, visitPage, waitForGraphqlOperation } from '../functions';

// All cluster-side assertions target the proxy's namespaces (which live on
// the same kind cluster as central in the chainsaw test setup).
//
// As with the standalone webapp suite, the cluster must start with 0
// Source / InstrumentationConfig CRDs; the chainsaw-test installs a fresh
// cluster, so this should hold.

const namespace = NAMESPACES.APPS;
const sourceCrdName = CRD_NAMES.SOURCE;
const configCrdName = CRD_NAMES.INSTRUMENTATION_CONFIG;
const totalEntities = SELECTED_ENTITIES.NAMESPACE_SOURCES.length;
const indexForUpdatedSource = 0;
const nameForUpdatedSource = SELECTED_ENTITIES.NAMESPACE_SOURCES[indexForUpdatedSource].name;

describe('Sources CRUD (via Central UI → Proxy)', () => {
  beforeEach(() => {
    cy.intercept('/graphql').as('gql');
    handleExceptions();
    signInAndSelectProxy();
  });

  it(`Should have 0 ${sourceCrdName} CRDs in the cluster`, () => {
    getCrdIds({ namespace, crdName: sourceCrdName, expectedError: TEXTS.NO_RESOURCES(namespace), expectedLength: 0 });
  });

  it(`Should have 0 ${configCrdName} CRDs in the cluster`, () => {
    getCrdIds({ namespace, crdName: configCrdName, expectedError: TEXTS.NO_RESOURCES(namespace), expectedLength: 0 });
  });

  it(`Should instrument ${totalEntities} sources via the Central UI`, () => {
    visitPage(ROUTES.OVERVIEW, () => {
      cy.get(DATA_IDS.ADD_SOURCE).click();

      cy.get(DATA_IDS.SELECT_NAMESPACE).should('exist').click();

      cy.wait(500).then(() => {
        cy.get('[data-id=workloads-select-all]').click();

        SELECTED_ENTITIES.NAMESPACE_SOURCES.forEach(({ name }) => {
          cy.get(DATA_IDS.SELECT_SOURCE(name)).should('exist');
        });

        cy.get(DATA_IDS.WIDE_DRAWER_SAVE).click();

        cy.get(DATA_IDS.WIDE_DRAWER_SAVE).should('not.exist');
      });
    });
  });

  it(`Should have 1 ${sourceCrdName} CRD in the cluster (namespace-level)`, () => {
    getCrdIds({ namespace, crdName: sourceCrdName, expectedError: '', expectedLength: 1 });
  });

  it(`Should have >= ${totalEntities} ${configCrdName} CRDs in the cluster`, () => {
    cy.exec(`kubectl get ${configCrdName} -n ${namespace} | awk 'NR>1 {print $1}'`).then(({ stdout }) => {
      const count = stdout.split('\n').filter((s) => !!s).length;
      expect(count).to.be.gte(totalEntities);
    });
  });

  it(`Should update the name of 1 source via the v2 edit-source-drawer, and notify locally`, () => {
    visitPage(ROUTES.OVERVIEW, () => {
      SELECTED_ENTITIES.NAMESPACE_SOURCES.slice(indexForUpdatedSource, indexForUpdatedSource + 1).forEach(({ name, kind }) => {
        updateEntity(
          {
            nodeId: DATA_IDS.SOURCE_NODE({ namespace, name, kind }),
            fieldKey: DATA_IDS.SOURCE_TITLE,
            fieldValue: TEXTS.UPDATED_NAME,
          },
          () => {
            awaitToast({ message: TEXTS.NOTIF_SOURCE_UPDATING });
            waitForGraphqlOperation('UpdateK8sActualSource').then(() => {
              awaitToast({ message: TEXTS.NOTIF_UPDATED });
            });
          },
        );
      });
    });
  });

  it(`Should update "otelServiceName" of 1 ${sourceCrdName} CRD in the cluster`, () => {
    findCrdId({ namespace, crdName: sourceCrdName, targetKey: 'workload.name', targetValue: nameForUpdatedSource }, (crdId) => {
      getCrdById({ namespace, crdName: sourceCrdName, crdId, expectedError: '', expectedKey: 'otelServiceName', expectedValue: TEXTS.UPDATED_NAME });
    });
  });

  it(`Should update "serviceName" of 1 ${configCrdName} CRD in the cluster`, () => {
    cy.exec(`kubectl get ${configCrdName} -n ${namespace} | awk 'NR>1 {print $1}'`).then(({ stdout }) => {
      const crdIds = stdout.split('\n').filter((s) => !!s);
      expect(crdIds.length).to.be.gte(totalEntities);

      const crdId = crdIds.find((id) => id.indexOf(nameForUpdatedSource) !== -1) || '';
      getCrdById({ namespace, crdName: configCrdName, crdId, expectedError: '', expectedKey: 'serviceName', expectedValue: TEXTS.UPDATED_NAME });
    });
  });

  it('Should uninstrument all sources via kubectl', () => {
    cy.exec(`kubectl delete ${sourceCrdName} --all -n ${namespace}`, { failOnNonZeroExit: false });
    cy.exec(`kubectl delete ${configCrdName} --all -n ${namespace}`, { failOnNonZeroExit: false });

    cy.wait(3000).then(() => {
      getCrdIds({ namespace, crdName: sourceCrdName, expectedError: TEXTS.NO_RESOURCES(namespace), expectedLength: 0 });
    });
  });

  it(`Should have 0 ${configCrdName} CRDs in the cluster`, () => {
    getCrdIds({ namespace, crdName: configCrdName, expectedError: TEXTS.NO_RESOURCES(namespace), expectedLength: 0 });
  });
});
