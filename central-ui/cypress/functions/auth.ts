import { AUTH_USER, DATA_IDS, PROXY, ROUTES } from '../constants';

// Auth-only session (used by connection specs that exercise /connections
// themselves). Proxy-aware CRUD specs use SESSION_WITH_PROXY below.
const SESSION_AUTH = `central-ui-admin-${AUTH_USER.EMAIL}`;
const SESSION_WITH_PROXY = `central-ui-admin-proxy-${AUTH_USER.EMAIL}-${PROXY.NAME}`;

const assertAuthPersisted = (win: Window) => {
  const raw = win.localStorage.getItem('odigos-auth-storage');
  expect(raw, 'odigos-auth-storage in localStorage').to.be.a('string');
  const parsed = JSON.parse(raw as string);
  expect(parsed?.state?.accessToken, 'persisted accessToken').to.be.a('string');
};

const assertProxyPersisted = (win: Window) => {
  // useProxyStore persists to sessionStorage (not localStorage).
  const raw = win.sessionStorage.getItem('SELECTED_PROXY');
  expect(raw, 'SELECTED_PROXY in sessionStorage').to.be.a('string');
  const parsed = JSON.parse(raw as string);
  expect(parsed?.state?.proxyName, 'persisted proxyName').to.eq(PROXY.NAME);
  expect(parsed?.state?.proxyID, 'persisted proxyID').to.be.a('string');
};

// Performs the email/password sign-in flow against the live central-backend.
//
// On the first call ever (no admin user exists yet), the central-backend
// treats the call as a first-time-setup and creates this user as the first
// admin. The form renders with a "Confirm password" field in that mode.
//
// Subsequent runs (e.g. cluster reused across spec files in the same chainsaw
// run) hit the regular login path with no confirm-password field.
const performSignIn = () => {
  cy.visit(ROUTES.SIGN_IN, { retryOnNetworkFailure: true });

  // Wait for the "Choose login method" form to render.
  cy.contains('Continue with Email', { timeout: 30000 }).should('be.visible').click();

  // Email + password are always present.
  cy.get(DATA_IDS.AUTH_EMAIL, { timeout: 30000 }).should('be.visible').clear().type(AUTH_USER.EMAIL);
  cy.get(DATA_IDS.AUTH_PASSWORD).clear().type(AUTH_USER.PASSWORD, { log: false });

  // First-time setup also asks for confirm password — fill it if visible.
  cy.get('body').then(($body) => {
    if ($body.find(`[data-id=confirmPassword]`).length > 0) {
      cy.get(DATA_IDS.AUTH_CONFIRM_PASSWORD).clear().type(AUTH_USER.PASSWORD, { log: false });
    }
  });

  // Submit (button label is "Sign In" or "Sign Up" depending on first-time).
  cy.get('form button[type=submit]').click();

  // ProtectedRoute redirects authenticated users to /connections.
  cy.location('pathname', { timeout: 60000 }).should('eq', ROUTES.CONNECTIONS);
};

// Picks the test proxy from the connections table. Assumes we are already on
// /connections (post sign-in). Visiting /connections intentionally clears the
// proxy store (useProxy treats it as an out-of-proxy page), so this must only
// run when we mean to re-select — never as a per-test warm-up.
const performSelectTestProxy = () => {
  // The ui-kit Table renders a hidden MeasurementLayer (visibility: hidden)
  // that duplicates cell text BEFORE the visible row in the DOM. Plain
  // `cy.contains(...)` resolves to that hidden duplicate and fails visibility
  // / click assertions, so scope these lookups to `:visible` elements only.
  cy.contains(':visible', PROXY.NAME, { timeout: 30000 }).should('be.visible');
  cy.contains(':visible', 'Connection live', { timeout: 30000 }).should('be.visible');
  // Click the real table row (Td onClick → onClickConnection). Text-only
  // clicks can still miss the row handler when MeasurementLayer duplicates
  // sit earlier in the DOM; data-table-row-index is on the interactive Row.
  cy.get('[data-table-row-index="0"]', { timeout: 30000 }).should('be.visible').click();

  // selectProxy() pushes us to /overview and writes SELECTED_PROXY.
  cy.location('pathname', { timeout: 30000 }).should('eq', ROUTES.OVERVIEW);
  cy.window().should(assertProxyPersisted);
};

// Caches the admin login across tests via cy.session(), so individual specs
// don't have to re-run the sign-in flow on each test. The session is shared
// across spec files (cacheAcrossSpecs: true) for the duration of the run.
export const signInAsAdmin = () => {
  cy.session(
    SESSION_AUTH,
    () => {
      performSignIn();
    },
    {
      cacheAcrossSpecs: true,
      validate: () => {
        cy.window().then(assertAuthPersisted);
      },
    },
  );
};

// Picks the test proxy from the connections table by its display name and
// waits for the in-proxy overview page to render.
//
// Prefer `signInAndSelectProxy()` for CRUD specs — that folds selection into
// cy.session so we don't visit /connections (and clear the proxy store) on
// every test. Keep this helper for specs that intentionally re-select.
export const selectTestProxy = () => {
  cy.visit(ROUTES.CONNECTIONS, { retryOnNetworkFailure: true });
  performSelectTestProxy();
};

// Convenience helper for specs that need signed-in + proxy-selected state.
//
// Root cause this addresses: every beforeEach used to call selectTestProxy(),
// which visits /connections. That path is an out-of-proxy page, so useProxy
// clears SELECTED_PROXY, then we re-select and reload overview — ~N full
// round-trips per suite against a single kubectl port-forward. Folding proxy
// selection into cy.session restores SELECTED_PROXY from sessionStorage;
// paired with useProxy waiting for persist hydration before clearProxy, each
// test can visit /overview|/settings directly.
export const signInAndSelectProxy = () => {
  cy.session(
    SESSION_WITH_PROXY,
    () => {
      performSignIn();
      // Sign-in lands on /connections; select without an extra visit.
      performSelectTestProxy();
    },
    {
      cacheAcrossSpecs: true,
      validate: () => {
        cy.window().then((win) => {
          assertAuthPersisted(win);
          assertProxyPersisted(win);
        });
      },
    },
  );
};
