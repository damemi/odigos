import { PROXY, ROUTES } from '../constants';
import { handleExceptions, signInAsAdmin, visitPage, waitForGraphqlOperation } from '../functions';

// The Central UI lists every Central Proxy as a row in /connections. Until a
// row is clicked, the user is on an out-of-proxy page and cannot reach the
// usual Sources / Destinations / Actions / Rules / Sampling flows.
//
// In our chainsaw setup we install exactly one proxy with `clusterName=
// PROXY.NAME`, so we click that row and assert the redirect to /overview.

describe('Select Connection', () => {
  beforeEach(() => {
    cy.intercept('/graphql').as('gql');
    handleExceptions();
    signInAsAdmin();
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
      cy.contains('Connection live', { timeout: 60000 }).should('be.visible');
    });
  });

  // testIsolation: true clears localStorage between `it()` blocks, so the
  // SELECTED_PROXY assertion is inlined in the same test that triggers the
  // selection.
  it('Should select the proxy, redirect to /overview, and persist the SELECTED_PROXY', () => {
    visitPage(ROUTES.CONNECTIONS, () => {
      cy.contains(PROXY.NAME, { timeout: 60000 }).should('be.visible');
      cy.contains('Connection live', { timeout: 60000 }).should('be.visible');
      cy.contains(PROXY.NAME).click();

      cy.location('pathname', { timeout: 30000 }).should('eq', ROUTES.OVERVIEW);

      cy.window().then((win) => {
        const raw = win.localStorage.getItem('SELECTED_PROXY');
        expect(raw, 'SELECTED_PROXY in localStorage').to.be.a('string');
        const parsed = JSON.parse(raw as string);
        expect(parsed?.state?.proxyName, 'persisted proxyName').to.eq(PROXY.NAME);
        expect(parsed?.state?.proxyID, 'persisted proxyID').to.be.a('string');
      });
    });
  });
});
