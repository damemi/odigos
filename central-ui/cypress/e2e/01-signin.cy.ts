import { AUTH_USER, DATA_IDS, ROUTES } from '../constants';
import { handleExceptions, interceptGraphql, visitPage, waitForGraphqlOperation } from '../functions';

// First-spec only: drives the email/password sign-in flow exactly as the
// user would, including first-time setup (when the central-backend has no
// admin user yet, the AuthForm renders with a Confirm-password field and
// the SignIn mutation creates the first admin account).
//
// Subsequent specs reuse the auth state via `cy.session()` (see auth.ts).

describe('Sign In', () => {
  beforeEach(() => {
    handleExceptions();
    interceptGraphql();
  });

  it('Should fetch system config (frontend ↔ central-backend connection)', () => {
    visitPage(ROUTES.SIGN_IN, () => {
      waitForGraphqlOperation('GetSystemConfig').then(() => {
        cy.contains('Continue with Email').should('be.visible');
      });
    });
  });

  // testIsolation: true clears localStorage between `it()` blocks, so we
  // assert auth-storage persistence inside the same test that performs the
  // sign-in. (Across-spec persistence is handled separately by cy.session
  // in `signInAsAdmin`.)
  it('Should sign in (or first-time-register) with admin credentials, land on /connections, and persist the access token', () => {
    visitPage(ROUTES.SIGN_IN, () => {
      cy.contains('Continue with Email').should('be.visible').click();

      cy.get(DATA_IDS.AUTH_EMAIL, { timeout: 30000 }).should('be.visible').clear().type(AUTH_USER.EMAIL);
      cy.get(DATA_IDS.AUTH_PASSWORD).clear().type(AUTH_USER.PASSWORD, { log: false });

      // First-time setup also asks for confirm-password — fill if visible.
      cy.get('body').then(($body) => {
        if ($body.find(`[data-id=confirmPassword]`).length > 0) {
          cy.get(DATA_IDS.AUTH_CONFIRM_PASSWORD).clear().type(AUTH_USER.PASSWORD, { log: false });
        }
      });

      cy.get('form button[type=submit]').click();

      waitForGraphqlOperation('SignIn').then(() => {
        cy.location('pathname', { timeout: 60000 }).should('eq', ROUTES.CONNECTIONS);

        cy.window().then((win) => {
          const raw = win.localStorage.getItem('odigos-auth-storage');
          expect(raw, 'odigos-auth-storage in localStorage').to.be.a('string');
          const parsed = JSON.parse(raw as string);
          expect(parsed?.state?.accessToken, 'persisted accessToken').to.be.a('string');
          expect(parsed?.state?.user?.email, 'persisted user email').to.eq(AUTH_USER.EMAIL);
        });
      });
    });
  });
});
