import { PROXY, ROUTES } from '../constants';
import { DEAD_PROXY, awaitToast, deleteConnectionViaUi, handleExceptions, interceptGraphql, interceptGraphqlWithDeadConnection, signInAsAdmin, visitPage, waitForGraphqlOperation } from '../functions';

// The Central UI lists every Central Proxy as a row in /connections. Until a
// row is clicked, the user is on an out-of-proxy page and cannot reach the
// usual Sources / Destinations / Actions / Rules / Sampling flows.
//
// In our chainsaw setup we install exactly one proxy with `clusterName=
// PROXY.NAME`, so we click that row and assert the redirect to /overview.

describe('Select Connection', () => {
  beforeEach(() => {
    handleExceptions();
    signInAsAdmin();
    interceptGraphql();
  });

  it('Should fetch compute platforms (proxy connections) on /connections', () => {
    visitPage(ROUTES.CONNECTIONS, () => {
      waitForGraphqlOperation('GetComputePlatforms').then(() => {
        cy.contains(':visible', PROXY.NAME, { timeout: 60000 }).should('be.visible');
      });
    });
  });

  it(`Should show the test proxy "${PROXY.NAME}" as a live connection`, () => {
    visitPage(ROUTES.CONNECTIONS, () => {
      cy.contains(':visible', PROXY.NAME, { timeout: 60000 }).should('be.visible');
      cy.contains(':visible', 'Connection live', { timeout: 60000 }).should('be.visible');
    });
  });

  // testIsolation: true clears localStorage between `it()` blocks, so the
  // SELECTED_PROXY assertion is inlined in the same test that triggers the
  // selection.
  it('Should select the proxy, redirect to /overview, and persist the SELECTED_PROXY', () => {
    visitPage(ROUTES.CONNECTIONS, () => {
      cy.contains(':visible', PROXY.NAME, { timeout: 60000 }).should('be.visible');
      cy.contains(':visible', 'Connection live', { timeout: 60000 }).should('be.visible');
      // A live connection's row opens it; the test proxy's id isn't known here, so find its row by name.
      cy.contains('[data-id^="connection-row-"]', PROXY.NAME, { timeout: 60000 }).should('be.visible').click();

      cy.location('pathname', { timeout: 30000 }).should('eq', ROUTES.OVERVIEW);

      // `useProxyStore` is configured with zustand's persist middleware
      // backed by `sessionStorage` (see `central-ui/src/store/useProxyStore.ts`),
      // not localStorage. Persistence is async (a microtask after the state
      // mutation), so the redirect to /overview can race the write —
      // `cy.window().should(callback)` retries until the store hand-off lands.
      cy.window().should((win) => {
        const raw = win.sessionStorage.getItem('SELECTED_PROXY');
        expect(raw, 'SELECTED_PROXY in sessionStorage').to.be.a('string');
        const parsed = JSON.parse(raw as string);
        expect(parsed?.state?.proxyName, 'persisted proxyName').to.eq(PROXY.NAME);
        expect(parsed?.state?.proxyID, 'persisted proxyID').to.be.a('string');
      });
    });
  });
});

// Regression for PLAT-1316: DeleteProxyConnection must send variables.proxyID.
// Sending `{ id }` (or omitting proxyID) yields HTTP 422 / GRAPHQL_VALIDATION_FAILED.
//
// Driven through the real UI path: the row's menu → Delete connection → approve
// (see deleteConnectionViaUi).
describe('Delete Connection (PLAT-1316)', () => {
  beforeEach(() => {
    handleExceptions();
    signInAsAdmin();
    interceptGraphqlWithDeadConnection();
  });

  it('Should send DeleteProxyConnection with variables.proxyID (not id)', () => {
    visitPage(ROUTES.CONNECTIONS, () => {
      waitForGraphqlOperation('GetComputePlatforms').then(() => {
        deleteConnectionViaUi(DEAD_PROXY.ID);

        waitForGraphqlOperation('DeleteProxyConnection').then((interception) => {
          const raw = interception.request.body;
          const body = typeof raw === 'string' ? JSON.parse(raw) : Array.isArray(raw) ? raw[0] : raw;

          expect(body?.operationName, 'operationName').to.eq('DeleteProxyConnection');
          expect(body?.variables, 'variables').to.deep.equal({ proxyID: DEAD_PROXY.ID });
          expect(body?.variables, 'must not send legacy `id` key').to.not.have.property('id');

          awaitToast({ message: 'Connection deleted successfully' });
        });
      });
    });
  });
});
