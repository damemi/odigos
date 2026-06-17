export * from './cy-alias';
export * from './auth';
import { DATA_IDS } from '../constants';

// Apollo may send a single object or a batch array.
const parseBody = (raw: unknown): { operationName?: string; variables?: { query?: string } } | undefined => {
  if (raw == null) return undefined;
  if (typeof raw === 'string') {
    try {
      return parseBody(JSON.parse(raw));
    } catch {
      return undefined;
    }
  }
  if (typeof raw !== 'object') return undefined;
  if (Array.isArray(raw)) {
    return parseBody(raw[0]);
  }
  return raw as { operationName?: string; variables?: { query?: string } };
};

const graphqlOperationName = (raw: unknown): string | undefined => {
  return parseBody(raw)?.operationName;
};

// Resolves only when the named operation completes.
//
// Supports both top-level Central GraphQL operations (e.g. SignIn,
// GetComputePlatforms) and "inner" operations sent through `RemoteFetch`
// (e.g. GetNamespacesWithWorkloads, UpdateK8sActualSource, CreateAction).
//
// When the operation isn't found on the outer RemoteFetch envelope, the
// inner query string is inspected for `query <Name>` / `mutation <Name>`.
//
// 30s per `cy.wait('@gql')` keeps the worst-case per-spec hang bounded:
// at the default 80 skips × 30s a missing operation surfaces in well
// under the chainsaw step budget.
export const waitForGraphqlOperation = (operationName: string, maxSkips = 80): Cypress.Chainable => {
  return cy.wait('@gql', { timeout: 30000 }).then((interception) => {
    const body = parseBody(interception.request.body);
    const op = body?.operationName;

    let matches = op === operationName;

    if (!matches && op === 'RemoteFetch') {
      const innerQuery = body?.variables?.query || '';
      const re = new RegExp(`(query|mutation)\\s+${operationName}\\b`);
      matches = re.test(innerQuery);
    }

    if (matches) {
      return cy.wrap(interception);
    }
    if (maxSkips <= 0) {
      throw new Error(`waitForGraphqlOperation: expected "${operationName}", got "${op ?? 'unknown'}" (no more skips)`);
    }
    return waitForGraphqlOperation(operationName, maxSkips - 1);
  });
};

export const visitPage = (path: string, callback?: () => void) => {
  cy.visit(path);

  cy.wait(1000).then(() => {
    if (!!callback) callback();
  });
};

interface FindCrdOptions {
  namespace: string;
  crdName: string;
  targetKey: string;
  targetValue: string;
}

export const findCrdId = ({ namespace, crdName, targetKey, targetValue }: FindCrdOptions, callback: (crdId: string) => void) => {
  const [parentKey, childKey] = targetKey.split('.');

  cy.exec(`kubectl get ${crdName} -n ${namespace} | awk 'NR>1 {print $1}'`).then(({ stdout }) => {
    const crdIds = stdout.split('\n').filter((str) => !!str);

    crdIds.forEach((crdId) => {
      cy.exec(`kubectl get ${crdName} ${crdId} -n ${namespace} -o json`).then(({ stdout }) => {
        const parsed = JSON.parse(stdout);
        const { spec } = parsed?.items?.[0] || parsed || {};
        expect(spec).to.not.be.empty;

        const value = childKey ? spec[parentKey][childKey] : spec[parentKey];
        if (value === targetValue) callback(crdId);
      });
    });
  });
};

interface GetCrdIdsOptions {
  namespace: string;
  crdName: string;
  expectedError: string;
  expectedLength: number;
}

// In central-ui the create/update/delete path goes Central UI → central-backend
// → WebSocket → central-proxy → proxy GraphQL → CRD apply, so `kubectl get`
// can race with the propagation tail. Poll until the assertion is satisfied
// instead of asserting once.
//
// `maxAttempts × intervalMs` = total budget. 30s is comfortably above the
// observed lag on slower clusters (1.21.12 / 1.23) without dragging out the
// happy path on faster ones (it short-circuits on the first matching attempt).
export const getCrdIds = ({ namespace, crdName, expectedError, expectedLength }: GetCrdIdsOptions, callback?: (crdIds: string[]) => void, maxAttempts = 15, intervalMs = 2000) => {
  const attempt = (remaining: number): Cypress.Chainable => {
    return cy.exec(`kubectl get ${crdName} -n ${namespace} | awk 'NR>1 {print $1}'`, { failOnNonZeroExit: false }).then(({ stderr, stdout }) => {
      const stderrMatches = stderr === expectedError;
      const stdoutMatches = expectedError ? stdout === '' : stdout !== '';
      const crdIds = stdout.split('\n').filter((str) => !!str);
      const lengthMatches = crdIds.length === expectedLength;

      if (stderrMatches && stdoutMatches && lengthMatches) {
        if (callback) callback(crdIds);
        return cy.wrap(null);
      }

      if (remaining <= 0) {
        // Re-run the asserts so cypress reports the actual mismatch.
        expect(stderr).to.eq(expectedError);
        if (expectedError) {
          expect(stdout).to.eq('');
        } else {
          expect(stdout).to.not.be.empty;
        }
        expect(crdIds.length).to.eq(expectedLength);
        if (callback) callback(crdIds);
        return cy.wrap(null);
      }

      return cy.wait(intervalMs).then(() => attempt(remaining - 1));
    });
  };

  return attempt(maxAttempts);
};

interface GetCrdByIdOptions {
  namespace: string;
  crdName: string;
  crdId: string;
  expectedError: string;
  expectedKey: string;
  expectedValue: string | boolean;
  // When true, asserts `spec[expectedKey]` *contains* `expectedValue` (substring)
  // instead of equalling it. Used when CRDs are renamed to per-entity unique
  // values (e.g. `${UPDATED_NAME} ${actionType}`) but the test only needs to
  // verify the shared marker substring landed on every CRD.
  expectedValueContains?: boolean;
}

export const getCrdById = ({ namespace, crdName, crdId, expectedError, expectedKey, expectedValue, expectedValueContains }: GetCrdByIdOptions, callback?: () => void) => {
  if (!crdId) {
    throw new Error('No CRD ID provided to getCrdById');
  }

  cy.exec(`kubectl get ${crdName} ${crdId} -n ${namespace} -o json`).then(({ stderr, stdout }) => {
    expect(stderr).to.eq(expectedError);

    if (!!expectedError) {
      expect(stdout).to.eq('');
    } else {
      expect(stdout).to.not.be.empty;
    }

    const parsed = JSON.parse(stdout);
    const { spec } = parsed?.items?.[0] || parsed || {};

    expect(spec).to.not.be.empty;
    if (expectedValueContains) {
      expect(spec[expectedKey]).to.include(expectedValue);
    } else {
      expect(spec[expectedKey]).to.eq(expectedValue);
    }

    if (!!callback) callback();
  });
};

interface UpdateEntityOptions {
  nodeId: string;
  nodeContains?: string;
  fieldKey: string;
  fieldValue: string;
}

export const updateEntity = ({ nodeId, nodeContains, fieldKey, fieldValue }: UpdateEntityOptions, callback?: () => void) => {
  if (!!nodeContains) {
    cy.contains(nodeId, nodeContains).should('exist').click();
  } else {
    cy.get(nodeId).should('exist').click();
  }

  cy.get(DATA_IDS.DRAWER).should('exist');
  cy.get(DATA_IDS.DRAWER_EDIT).click();

  cy.get(fieldKey).click().focused().clear().type(fieldValue);
  cy.get(fieldKey).should('have.value', fieldValue);

  cy.get(DATA_IDS.DRAWER_SAVE).click();
  cy.get(DATA_IDS.DRAWER_CLOSE).click();
  if (!!callback) callback();
};

interface DeleteEntityOptions {
  nodeId: string;
  nodeContains: string;
  warnModalTitle?: string;
  warnModalNote?: string;
}

export const deleteEntity = ({ nodeId, nodeContains, warnModalTitle, warnModalNote }: DeleteEntityOptions, callback?: () => void) => {
  cy.contains(nodeId, nodeContains).should('exist').click();
  cy.get(DATA_IDS.DRAWER).should('exist');
  cy.get(DATA_IDS.DRAWER_DELETE).click();

  if (!!warnModalTitle) cy.get(DATA_IDS.MODAL).contains(warnModalTitle).should('exist');
  if (!!warnModalNote) cy.get(DATA_IDS.MODAL).contains(warnModalNote).should('exist');

  cy.get(DATA_IDS.APPROVE).click();

  if (!!callback) callback();
};

interface UpdateV2EntityOptions {
  // Where to click to open the drawer — either a selector or, when paired with `nodeContains`,
  // an HTML tag passed to `cy.contains()` (e.g. `'div'`).
  nodeId: string;
  nodeContains?: string;

  // Drawer convention prefix. Together with the `{prefix}-btn-{edit,delete,save,cancel}` naming,
  // this identifies the dropdown options and footer buttons rendered by a v2 edit drawer.
  // E.g. `'edit-rule'`, `'edit-action'`, `'edit-source'`, `'edit-destination'`.
  prefix: string;

  // Selector + value of the form field to update inside the drawer.
  fieldKey: string;
  fieldValue: string;
}

// v2 edit-drawer flow:
// 1. open the entity's drawer by clicking its data-flow row
// 2. open the drawer header's actions dropdown and click "Edit"
// 3. update the named field and click "Save"
// 4. close the drawer (it does not auto-close after save)
export const updateV2Entity = ({ nodeId, nodeContains, prefix, fieldKey, fieldValue }: UpdateV2EntityOptions, callback?: () => void) => {
  if (!!nodeContains) {
    cy.contains(nodeId, nodeContains).should('exist').click();
  } else {
    cy.get(nodeId).should('exist').click();
  }

  cy.get(DATA_IDS.DRAWER).should('exist');

  cy.get(DATA_IDS.DRAWER_ACTIONS).click();
  cy.get(DATA_IDS.DROPDOWN_OPTION(`${prefix}-btn-edit`)).click();

  cy.get(fieldKey).click().focused().clear().type(fieldValue);
  cy.get(fieldKey).should('have.value', fieldValue);

  cy.get(`[data-id=${prefix}-btn-save]`).click();

  if (!!callback) callback();

  cy.get(DATA_IDS.DRAWER_CLOSE).click();
};

interface DeleteV2EntityOptions {
  nodeId: string;
  nodeContains?: string;
  prefix: string;
  warnModalTitle?: string;
  warnModalNote?: string;
}

// v2 edit-drawer flow: open drawer → actions dropdown → "Delete" → confirm modal.
export const deleteV2Entity = ({ nodeId, nodeContains, prefix, warnModalTitle, warnModalNote }: DeleteV2EntityOptions, callback?: () => void) => {
  // Don't insert `.should('exist')` between the query and `.click()`: it
  // settles the subject and disables Cypress's built-in query re-resolution,
  // so a re-render of the overview (frequent here via the proxy round-trip +
  // SSE-driven refetches) detaches the node mid-click ("the page updated
  // while this command was executing"). Clicking straight off the query keeps
  // the chain retryable, and `.click()` already asserts existence/visibility.
  if (!!nodeContains) {
    cy.contains(nodeId, nodeContains).click();
  } else {
    cy.get(nodeId).click();
  }

  cy.get(DATA_IDS.DRAWER).should('exist');

  cy.get(DATA_IDS.DRAWER_ACTIONS).click();
  cy.get(DATA_IDS.DROPDOWN_OPTION(`${prefix}-btn-delete`)).click();

  if (!!warnModalTitle) cy.get(DATA_IDS.MODAL).contains(warnModalTitle).should('exist');
  if (!!warnModalNote) cy.get(DATA_IDS.MODAL).contains(warnModalNote).should('exist');

  cy.get(DATA_IDS.APPROVE).click();

  if (!!callback) callback();
};

interface AwaitToastOptions {
  message: string;
}

export const awaitToast = ({ message }: AwaitToastOptions, callback?: () => void) => {
  // ui-kit >=0.0.244 dropped the stable `data-id=toast` wrapper, so we anchor
  // on the close button (the only stable selector left) and filter to the
  // toast whose subtree contains the expected message. This avoids matching
  // unrelated elements that happen to render the same text.
  cy.get(DATA_IDS.TOAST_CLOSE, { timeout: 10000 })
    .filter(
      (_, el) =>
        Cypress.$(el)
          .parents()
          .filter((__, p) => (Cypress.$(p).text() || '').includes(message)).length > 0,
    )
    .first()
    .as('toast-close');

  cy.get('@toast-close').should('exist');
  cy.get('@toast-close').click({ force: true });

  callback?.();
};

// The sampling page in @odigos/ui-kit auto-opens an onboarding modal whenever
// it renders with zero sampling CRDs. The cypress sampling spec begins by
// wiping all sampling CRDs, so the modal pops up on the very first visit and
// blocks the create-rule button. Click "Skip" if the modal is currently
// rendered, otherwise no-op.
export const dismissSamplingOnboardingModal = () => {
  cy.get('body').then(($body) => {
    if ($body.find(DATA_IDS.SAMPLING_ONBOARDING_BTN_SKIP).length > 0) {
      cy.get(DATA_IDS.SAMPLING_ONBOARDING_BTN_SKIP).click();
    }
  });
};

export const handleExceptions = () => {
  return cy.on('uncaught:exception', (err) => {
    if (err.message.includes('ResizeObserver loop completed with undelivered notifications')) {
      return false;
    }
  });
};
