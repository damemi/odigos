import Cypress from 'cypress';
import fs from 'fs';

const PORT = 3000;
const BASE_URL = `http://localhost:${PORT}`;

const config: Cypress.ConfigOptions = {
  trashAssetsBeforeRuns: true,
  screenshotOnRunFailure: true,
  video: true,

  e2e: {
    baseUrl: BASE_URL,
    supportFile: false,
    waitForAnimations: true,
    animationDistanceThreshold: 5,
    viewportWidth: 1920,
    viewportHeight: 1080,
    // Tight timeouts so a flaky / hung test surfaces fast in CI rather
    // than running out the whole chainsaw step's clock.
    defaultCommandTimeout: 8000,
    pageLoadTimeout: 20000,
    requestTimeout: 10000,
    responseTimeout: 10000,
    retries: { runMode: 0, openMode: 0 },
    allowCypressEnv: false,
    // Central UI persists auth + selected proxy in localStorage; keeping
    // testIsolation enabled clears those between tests, so we re-establish
    // the session via cy.session() in the shared helpers.
    testIsolation: true,
    setupNodeEvents(on, config) {
      on('task', {
        log: (message) => {
          console.log(message);
          return null;
        },
      });

      on('after:spec', (spec: Cypress.Spec, results: CypressCommandLine.RunResult) => {
        if (results && results.video) {
          const failures = results.tests.some((test) => test.attempts.some((attempt) => attempt.state === 'failed'));

          if (!failures) {
            fs.unlinkSync(results.video);
          }
        }
      });
    },
  },
};

export default Cypress.defineConfig(config);
