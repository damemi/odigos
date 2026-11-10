import Cypress from 'cypress';
import { exec as execCb } from 'child_process';
import { promisify } from 'util';
import fs from 'fs';

const execAsync = promisify(execCb);

const PORT = 3000;
// Chainsaw e2e prefers a kind NodePort URL (CYPRESS_BASE_URL) so Cypress does
// not depend on a long-lived kubectl port-forward that wedges under load.
const BASE_URL = process.env.CYPRESS_BASE_URL || `http://localhost:${PORT}`;

type ExecTaskArgs = {
  command: string;
  failOnNonZeroExit?: boolean;
};

type ExecTaskResult = {
  code: number;
  stdout: string;
  stderr: string;
};

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
    // Tight command timeouts so a hung assertion surfaces fast.
    defaultCommandTimeout: 8000,
    pageLoadTimeout: 45000,
    requestTimeout: 10000,
    responseTimeout: 10000,
    // No runMode retries — flakes should be fixed at the sync point, not
    // hidden by a second attempt that also burns the chainsaw budget.
    retries: { runMode: 0, openMode: 0 },
    allowCypressEnv: false,
    // Central UI persists auth in localStorage and selected proxy in
    // sessionStorage; testIsolation clears both between tests, so we
    // re-establish via cy.session() (auth + proxy) in the shared helpers.
    testIsolation: true,
    setupNodeEvents(on, config) {
      on('task', {
        log: (message) => {
          console.log(message);
          return null;
        },
        // Cypress 16 removed cy.exec(); shell out via task instead.
        // https://on.cypress.io/task
        // Match legacy cy.exec() by trimming trailing newlines from stdout/stderr.
        async exec({ command, failOnNonZeroExit = true }: ExecTaskArgs): Promise<ExecTaskResult> {
          const normalize = (s: string | undefined | null) => (s ?? '').replace(/\n+$/, '');
          try {
            const { stdout, stderr } = await execAsync(command, {
              shell: '/bin/bash',
              maxBuffer: 10 * 1024 * 1024,
            });
            return { code: 0, stdout: normalize(stdout), stderr: normalize(stderr) };
          } catch (error: unknown) {
            const err = error as { code?: number; stdout?: string; stderr?: string; message?: string };
            const result: ExecTaskResult = {
              code: typeof err.code === 'number' ? err.code : 1,
              stdout: normalize(err.stdout?.toString()),
              stderr: normalize(err.stderr?.toString() ?? err.message),
            };
            if (failOnNonZeroExit) {
              throw new Error(`exec failed (${result.code}): ${command}\nstderr: ${result.stderr}\nstdout: ${result.stdout}`);
            }
            return result;
          }
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
