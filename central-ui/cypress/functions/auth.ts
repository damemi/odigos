import { AUTH_USER, DATA_IDS, PROXY, ROUTES } from '../constants';

const SESSION_ID = `central-ui-admin-${AUTH_USER.EMAIL}`;

// Performs the email/password sign-in flow against the live central-backend.
//
// On the first call ever (no admin user exists yet), the central-backend
// treats the call as a first-time-setup and creates this user as the first
// admin. The form renders with a "Confirm password" field in that mode.
//
// Subsequent runs (e.g. cluster reused across spec files in the same chainsaw
// run) hit the regular login path with no confirm-password field.
const performSignIn = () => {
  cy.visit(ROUTES.SIGN_IN);

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

// Caches the admin login across tests via cy.session(), so individual specs
// don't have to re-run the sign-in flow on each test. The session is shared
// across spec files (cacheAcrossSpecs: true) for the duration of the run.
export const signInAsAdmin = () => {
  cy.session(
    SESSION_ID,
    () => {
      performSignIn();
    },
    {
      cacheAcrossSpecs: true,
      validate: () => {
        // Sanity check: the auth tokens should be persisted in localStorage.
        cy.window().then((win) => {
          const raw = win.localStorage.getItem('odigos-auth-storage');
          expect(raw, 'odigos-auth-storage in localStorage').to.be.a('string');
          const parsed = JSON.parse(raw as string);
          expect(parsed?.state?.accessToken, 'persisted accessToken').to.be.a('string');
        });
      },
    },
  );
};

// Picks the test proxy from the connections table by its display name and
// waits for the in-proxy overview page to render.
//
// Use after signInAsAdmin() in any spec that needs to interact with the
// usual Sources / Destinations / Actions / Rules / Sampling flows.
export const selectTestProxy = () => {
  cy.visit(ROUTES.CONNECTIONS);
  // Wait for the connections list to populate. The chainsaw setup installs
  // the proxy with clusterName=PROXY.NAME, which appears as a row in the
  // table. Status must be "Connection live" before clicking is allowed.
  cy.contains(PROXY.NAME, { timeout: 60000 }).should('be.visible');
  cy.contains('Connection live', { timeout: 60000 }).should('be.visible');
  cy.contains(PROXY.NAME).click();

  // selectProxy() pushes us to /overview.
  cy.location('pathname', { timeout: 30000 }).should('eq', ROUTES.OVERVIEW);
};

// Convenience helper for specs that just want a signed-in + proxy-selected
// state before each test.
export const signInAndSelectProxy = () => {
  signInAsAdmin();
  selectTestProxy();
};
