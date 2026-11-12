import { CRD_NAMES, DATA_IDS, NAMESPACES, ROUTES, SELECTED_ENTITIES, TEXTS } from '../constants';
import { awaitToast, clickRetryable, cyExec, findCrdId, getCrdById, getCrdIds, handleExceptions, interceptGraphql, signInAndSelectProxy, updateV2Entity, visitPage, waitForCrdCount, waitForGraphqlOperation } from '../functions';

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
    handleExceptions();
    signInAndSelectProxy();
    // Arm after bootstrap so overview RemoteFetch from session setup is not
    // queued under `@gql` (stale waits). See interceptGraphql().
    interceptGraphql();
  });

  it(`Should have 0 ${sourceCrdName} CRDs in the cluster`, () => {
    getCrdIds({ namespace, crdName: sourceCrdName, expectedError: TEXTS.NO_RESOURCES(namespace), expectedLength: 0 });
  });

  it(`Should have 0 ${configCrdName} CRDs in the cluster`, () => {
    getCrdIds({ namespace, crdName: configCrdName, expectedError: TEXTS.NO_RESOURCES(namespace), expectedLength: 0 });
  });

  it(`Should instrument ${totalEntities} sources via the Central UI`, () => {
    visitPage(ROUTES.OVERVIEW, () => {
      clickRetryable(DATA_IDS.ADD_SOURCE);

      // Re-resolving clicks: overview/SSE refetches detach nodes mid-click when
      // `.should('exist')` settles the subject before `.click()`.
      clickRetryable(DATA_IDS.SELECT_NAMESPACE);

      // Wait for the workloads column to populate before select-all. Clicking
      // `workloads-select-all` as soon as it mounts (before rows render) left
      // `source-*` checkboxes missing on slower clusters (K8s 1.23 flake).
      const firstSource = SELECTED_ENTITIES.NAMESPACE_SOURCES[0].name;
      cy.get(DATA_IDS.SELECT_SOURCE(firstSource), { timeout: 30000 }).should('exist');

      clickRetryable('[data-id=workloads-select-all]');

      SELECTED_ENTITIES.NAMESPACE_SOURCES.forEach(({ name }) => {
        cy.get(DATA_IDS.SELECT_SOURCE(name)).should('exist');
      });

      clickRetryable(DATA_IDS.WIDE_DRAWER_SAVE);

      cy.get(DATA_IDS.WIDE_DRAWER_SAVE).should('not.exist');
    });
  });

  it(`Should have 1 ${sourceCrdName} CRD in the cluster (namespace-level)`, () => {
    getCrdIds({ namespace, crdName: sourceCrdName, expectedError: '', expectedLength: 1 });
  });

  it(`Should have >= ${totalEntities} ${configCrdName} CRDs in the cluster`, () => {
    waitForCrdCount({ namespace, crdName: configCrdName, minCount: totalEntities });
  });

  it(`Should update the name of 1 source via the v2 edit-source-drawer, and notify locally`, () => {
    visitPage(ROUTES.OVERVIEW, () => {
      SELECTED_ENTITIES.NAMESPACE_SOURCES.slice(indexForUpdatedSource, indexForUpdatedSource + 1).forEach(({ name, kind }) => {
        updateV2Entity(
          {
            nodeId: DATA_IDS.SOURCE_NODE({ namespace, name, kind }),
            prefix: DATA_IDS.SOURCE_DRAWER_PREFIX,
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
    waitForCrdCount({ namespace, crdName: configCrdName, minCount: totalEntities }).then((crdIds) => {
      const crdId = crdIds.find((id) => id.indexOf(nameForUpdatedSource) !== -1) || '';
      getCrdById({ namespace, crdName: configCrdName, crdId, expectedError: '', expectedKey: 'serviceName', expectedValue: TEXTS.UPDATED_NAME });
    });
  });

  it('Should uninstrument all sources via kubectl', () => {
    cyExec(`kubectl delete ${sourceCrdName} --all -n ${namespace}`, { failOnNonZeroExit: false });
    cyExec(`kubectl delete ${configCrdName} --all -n ${namespace}`, { failOnNonZeroExit: false });

    getCrdIds({ namespace, crdName: sourceCrdName, expectedError: TEXTS.NO_RESOURCES(namespace), expectedLength: 0 });
  });

  it(`Should have 0 ${configCrdName} CRDs in the cluster`, () => {
    getCrdIds({ namespace, crdName: configCrdName, expectedError: TEXTS.NO_RESOURCES(namespace), expectedLength: 0 });
  });
});
