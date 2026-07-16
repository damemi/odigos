import { PROXY, ROUTES } from '../constants';
import { handleExceptions, interceptGraphql, signInAsAdmin, visitPage, waitForGraphqlOperation } from '../functions';

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
        cy.contains(PROXY.NAME, { timeout: 60000 }).should('be.visible');
      });
    });
  });

  it(`Should show the test proxy "${PROXY.NAME}" as a live connection`, () => {
    visitPage(ROUTES.CONNECTIONS, () => {
      cy.contains(PROXY.NAME, { timeout: 60000 }).should('be.visible');
      // The ui-kit Table renders a hidden MeasurementLayer that duplicates
      // text from auto-width column cells (e.g. the Status badge) BEFORE
      // the visible row in DOM order. Scope to `:visible` so we resolve the
      // real badge instead of the hidden duplicate.
      cy.contains(':visible', 'Connection live', { timeout: 60000 }).should('be.visible');
    });
  });

  // testIsolation: true clears localStorage between `it()` blocks, so the
  // SELECTED_PROXY assertion is inlined in the same test that triggers the
  // selection.
  it('Should select the proxy, redirect to /overview, and persist the SELECTED_PROXY', () => {
    visitPage(ROUTES.CONNECTIONS, () => {
      cy.contains(PROXY.NAME, { timeout: 60000 }).should('be.visible');
      cy.contains(':visible', 'Connection live', { timeout: 60000 }).should('be.visible');
      cy.contains(PROXY.NAME).click();

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
