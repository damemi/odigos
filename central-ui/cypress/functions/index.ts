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
  // retryOnNetworkFailure covers brief TCP blips without lengthening
  // pageLoadTimeout (long visit hangs cascade into skipped suites).
  //
  // Do NOT visit /connections here for CRUD warm-up: that path clears
  // SELECTED_PROXY (see useProxy isOutOfProxyPage). Auth+proxy come from
  // cy.session via signInAndSelectProxy().
  cy.visit(path, { retryOnNetworkFailure: true });

  if (callback) {
    callback();
  }
};

export type CyExecResult = { code: number; stdout: string; stderr: string };

/** Drop-in for removed Cypress 16 `cyExec()` — runs via `cy.task('exec')`. */
export const cyExec = (command: string, options?: { failOnNonZeroExit?: boolean }) => cy.task<CyExecResult>('exec', { command, failOnNonZeroExit: options?.failOnNonZeroExit ?? true });

// Arm the shared GraphQL alias AFTER auth/proxy bootstrap.
//
// Specs that called `cy.intercept('/graphql').as('gql')` before
// `signInAndSelectProxy()` queued overview RemoteFetch ops (including
// GetEffectiveConfig) under the same alias. Later `waitForGraphqlOperation
// ('GetEffectiveConfig')` could resolve that stale overview response while
// the settings form was still empty — the classic settings.cy clusterName flake.
export const interceptGraphql = () => {
  cy.intercept('/graphql').as('gql');
};

// Inject a dead connection + stub delete so PLAT-1316 can assert wire format
// without deleting the live proxy.
export const DEAD_PROXY = {
  ID: 'plat-1316-dead-proxy',
  NAME: 'dead-connection-plat-1316',
} as const;

export const interceptGraphqlWithDeadConnection = () => {
  cy.intercept('POST', '/graphql', (req) => {
    const body = parseBody(req.body) as { operationName?: string; variables?: Record<string, unknown> } | undefined;
    const op = body?.operationName;

    if (op === 'DeleteProxyConnection') {
      req.reply({
        data: {
          deleteProxyConnection: { success: true, error: null },
        },
      });
      return;
    }

    if (op === 'GetComputePlatforms') {
      req.continue((res) => {
        const platforms = res.body?.data?.computePlatforms;
        if (!Array.isArray(platforms)) return;
        if (platforms.some((p: { id?: string }) => p?.id === DEAD_PROXY.ID)) return;
        res.body.data.computePlatforms = [
          ...platforms,
          {
            id: DEAD_PROXY.ID,
            name: DEAD_PROXY.NAME,
            odigosVersion: 'v1.31.1',
            graphqlSchemaVersion: null,
            type: 'k8s',
            status: 'error',
            connectedAt: null,
            lastSeenAt: null,
            users: [],
            teams: [],
          },
        ];
      });
      return;
    }

    req.continue();
  }).as('gql');
};

type GetRowActions = (row: { cells: { key: string; rawValue: string }[] }) => Array<{ 'data-id'?: string; onClick?: () => void } | null>;

type FiberNode = {
  memoizedProps?: { getRowActions?: GetRowActions };
  pendingProps?: { getRowActions?: GetRowActions };
  return?: FiberNode | null;
} | null;

// HoverActions only mount while a row is hovered, and headless Cypress cannot
// reliably drive that hover (native mouseenter / .trigger often never flip
// React's onMouseEnter). Instead, resolve the same `getRowActions` the Table
// receives from CentralConnections and invoke the delete action's onClick —
// that opens DeleteModal exactly as the HoverAction would. Row presence is
// still asserted via kit `data-id=connection-row-{id}`; approve uses `approve`.
const findAllGetRowActions = (root: ParentNode): GetRowActions[] => {
  const found: GetRowActions[] = [];
  const seen = new Set<GetRowActions>();

  for (const el of Array.from(root.querySelectorAll('*'))) {
    const fiberKey = Object.keys(el).find((k) => k.startsWith('__reactFiber') || k.startsWith('__reactInternalInstance'));
    if (!fiberKey) continue;
    let fiber = (el as unknown as Record<string, FiberNode>)[fiberKey];
    while (fiber) {
      const fn = fiber.memoizedProps?.getRowActions ?? fiber.pendingProps?.getRowActions;
      if (typeof fn === 'function' && !seen.has(fn)) {
        seen.add(fn);
        found.push(fn);
      }
      fiber = fiber.return ?? null;
    }
  }
  return found;
};

/**
 * Delete a connection via the real UI path (row action → DeleteModal → approve).
 * Avoids hover: kit HoverActions unmount when not hovered, which flakes in CI.
 */
export const deleteConnectionViaUi = (proxyID: string) => {
  const rowSel = `[data-id="connection-row-${proxyID}"]`;
  const deleteDataId = `delete-connection-${proxyID}`;

  cy.get(rowSel, { timeout: 60000 }).should('be.visible');

  cy.document().then((doc) => {
    const candidates = findAllGetRowActions(doc.body);
    expect(candidates.length, 'Table getRowActions candidates').to.be.greaterThan(0);

    const fakeRow = {
      // Keys match ui-kit central-connections helpers (`Keys.Id` / `Keys.Status`).
      cells: [
        { key: 'id', rawValue: proxyID },
        { key: 'status', rawValue: 'error' },
      ],
    };

    let deleteAction: { 'data-id'?: string; onClick?: () => void } | null | undefined;
    for (const getRowActions of candidates) {
      const actions = getRowActions(fakeRow);
      deleteAction = (actions ?? []).find((a) => a?.['data-id'] === deleteDataId || a?.['data-id'] === 'delete-connection');
      if (typeof deleteAction?.onClick === 'function') break;
      deleteAction = undefined;
    }

    expect(deleteAction?.onClick, `HoverAction ${deleteDataId}`).to.be.a('function');
    deleteAction!.onClick!();
  });

  cy.get('[data-id=approve]', { timeout: 10000 }).should('be.visible').click();
};

// Click without inserting `.should('exist')` first: that settles the subject and
// disables Cypress query re-resolution, so an SSE/overview re-render detaches
// the node mid-click ("the page updated while this command was executing").
// `.click()` already asserts existence/visibility and keeps the get() retryable.
export const clickRetryable = (selector: string, options?: Partial<Cypress.Timeoutable>) => {
  cy.get(selector, options).click();
};

interface FindCrdOptions {
  namespace: string;
  crdName: string;
  targetKey: string;
  targetValue: string;
}

export const findCrdId = ({ namespace, crdName, targetKey, targetValue }: FindCrdOptions, callback: (crdId: string) => void) => {
  const [parentKey, childKey] = targetKey.split('.');

  cyExec(`kubectl get ${crdName} -n ${namespace} | awk 'NR>1 {print $1}'`).then(({ stdout }) => {
    const crdIds = stdout.split('\n').filter((str) => !!str);

    crdIds.forEach((crdId) => {
      cyExec(`kubectl get ${crdName} ${crdId} -n ${namespace} -o json`).then(({ stdout }) => {
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
    return cyExec(`kubectl get ${crdName} -n ${namespace} | awk 'NR>1 {print $1}'`, { failOnNonZeroExit: false }).then(({ stderr, stdout }) => {
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

interface WaitForCrdCountOptions {
  namespace: string;
  crdName: string;
  minCount: number;
}

// Like `getCrdIds`, but polls until the CRD count is >= minCount (create
// paths can lag behind UI toasts on slower clusters). Returns the matching
// id list so callers can select a specific CRD without a second kubectl.
export const waitForCrdCount = ({ namespace, crdName, minCount }: WaitForCrdCountOptions, maxAttempts = 15, intervalMs = 2000): Cypress.Chainable<string[]> => {
  const attempt = (remaining: number): Cypress.Chainable<string[]> => {
    return cyExec(`kubectl get ${crdName} -n ${namespace} | awk 'NR>1 {print $1}'`, { failOnNonZeroExit: false }).then(({ stdout }) => {
      const crdIds = stdout.split('\n').filter((str) => !!str);
      if (crdIds.length >= minCount) {
        return cy.wrap(crdIds);
      }
      if (remaining <= 0) {
        expect(crdIds.length).to.be.gte(minCount);
        return cy.wrap(crdIds);
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

  cyExec(`kubectl get ${crdName} ${crdId} -n ${namespace} -o json`).then(({ stderr, stdout }) => {
    expect(stderr).to.eq(expectedError);

    if (!!expectedError) {
      expect(stdout).to.eq('');
    } else {
      expect(stdout).to.not.be.empty;
    }

    const parsed = JSON.parse(stdout);
    const { spec } = parsed?.items?.[0] || parsed || {};

    expect(spec).to.not.be.empty;
    expect(spec[expectedKey], `spec.${expectedKey}`).to.not.eq(undefined);
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
    cy.contains(nodeId, nodeContains).click();
  } else {
    clickRetryable(nodeId);
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
  cy.contains(nodeId, nodeContains).click();
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

// ui-kit Drawer portals a full-screen Overlay sibling of `[data-id=drawer]`
// (no data-id on the Overlay itself). When isOpen flips false both unmount
// together — so "drawer gone" is the correct sync point. Waiting only on
// save/toast and then force-clicking the next row was papering over cases
// where close hadn't finished (or a toast still covered the row).
const waitForDrawerClosed = () => {
  cy.get(DATA_IDS.DRAWER, { timeout: 20000 }).should('not.exist');
};

const openOverviewEntity = (nodeId: string, nodeContains?: string) => {
  // Re-resolving click (no `.should('exist')` settle). Cypress retries
  // actionability until the row is uncovered — that is the real signal that
  // the previous drawer's Overlay is gone. Do not force:true.
  if (nodeContains) {
    cy.contains(nodeId, nodeContains, { timeout: 30000 }).click();
  } else {
    cy.get(nodeId, { timeout: 30000 }).click();
  }
};

// v2 edit-drawer flow:
// 1. open the entity's drawer by clicking its data-flow row
// 2. open the drawer header's actions dropdown and click "Edit"
// 3. update the named field and click "Save"
// 4. run post-save waits (mutation/toast), then close so the Overlay cannot
//    cover the next entity row
export const updateV2Entity = ({ nodeId, nodeContains, prefix, fieldKey, fieldValue }: UpdateV2EntityOptions, callback?: () => void) => {
  openOverviewEntity(nodeId, nodeContains);

  cy.get(DATA_IDS.DRAWER).should('exist');

  cy.get(DATA_IDS.DRAWER_ACTIONS).click();
  cy.get(DATA_IDS.DROPDOWN_OPTION(`${prefix}-btn-edit`)).click();

  cy.get(fieldKey).click().focused().clear().type(fieldValue);
  cy.get(fieldKey).should('have.value', fieldValue);

  cy.get(`[data-id=${prefix}-btn-save]`).click();

  if (callback) callback();

  // Edit* drawers keep isOpen while mounted. Header onClose is gated on
  // `isEditing && isFormDirty`: while that is true, the X opens a discard
  // warn modal instead of unmounting. Save flips isEditing off only after
  // the mutation await resolves and React commits — racing that commit was
  // leaving `[data-id=drawer]` mounted (seen as Overlay timeout in CI).
  cy.get(`[data-id=${prefix}-btn-save]`, { timeout: 20000 }).should('not.exist');

  cy.get(DATA_IDS.DRAWER_CLOSE).click();
  waitForDrawerClosed();
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
  openOverviewEntity(nodeId, nodeContains);

  cy.get(DATA_IDS.DRAWER).should('exist');

  cy.get(DATA_IDS.DRAWER_ACTIONS).click();
  cy.get(DATA_IDS.DROPDOWN_OPTION(`${prefix}-btn-delete`)).click();

  if (!!warnModalTitle) cy.get(DATA_IDS.MODAL).contains(warnModalTitle).should('exist');
  if (!!warnModalNote) cy.get(DATA_IDS.MODAL).contains(warnModalNote).should('exist');

  cy.get(DATA_IDS.APPROVE).click();

  if (callback) callback();

  waitForDrawerClosed();
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
