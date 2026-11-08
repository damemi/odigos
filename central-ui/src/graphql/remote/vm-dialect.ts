import { print, type DocumentNode } from 'graphql';
import { LATEST_DIALECT_VERSION, resolveDialect } from '@odigos/vmagent-api-apollo-client/registry';
import type { OperationContext } from '@odigos/ui-kit/contexts';
import { isParseableAgentsApiVersion, supportsTypedActionsAndRules } from '@/graphql/remote/utils';

// VM dialect resolution for the central-ui REMOTE_FETCH path.
//
// Unlike k8s (which floors a hand-written `VersionedRemoteFetch` bucket map via
// `getVersionedQuery`/`trimVersion`), VM proxies select their GraphQL document
// straight from the published `@odigos/vmagent-api-apollo-client` dialect
// registry. The package ships one frozen dialect per agents-api snapshot
// (v0.6.0 … v0.25.0); `resolveDialect(reported)` floors the proxy's reported
// schema version to the matching dialect (patch-aware, full semver) so the
// agent never receives fields its schema lacks. This keeps a single source of
// truth for VM operations and makes patch-level resolution automatic.

// Version used to pick the VM dialect. VM proxies report their agents-api schema
// version (`graphqlSchemaVersion`) separately from the vm-agent binary version,
// so prefer `schemaVersion` and fall back to `version` for older agents that
// don't report a schema version. Mirrors the adapter's k8s `dialectVersion`.
type ProxyOperationContext = OperationContext & { schemaVersion?: string };
export const dialectVersion = (ctx: OperationContext): string => {
  const reported = (ctx as ProxyOperationContext).schemaVersion ?? ctx.version;
  // Local OSX / `go run` agents report `(devel)`, `UNKNOWN-VERSION`, or the
  // binary string `development`. resolveDialect floors those to v0.6.0, which
  // has no Destination.disabled — UpdateDestination then omits the field and
  // the UI toasts success with no persist. Use the latest frozen dialect.
  if (!isParseableAgentsApiVersion(reported)) return LATEST_DIALECT_VERSION;
  return reported;
};

// A dialect is the union of every frozen snapshot's generated module, and the
// surface diverged across versions (v0.12.0 dropped the legacy `*V2` / k8s /
// namespace documents). The union therefore only guarantees the documents
// present in EVERY snapshot, so selectors reach for documents by name through a
// loose view; `resolveDialect`'s floor guarantees the chosen snapshot actually
// carries the document for the connected proxy.
type DialectDocs = Record<string, DocumentNode>;

// Picks the operation document out of the resolved dialect, e.g.
// `(d) => d.GetSourcesDocument`.
export type VmSelect = (dialect: DialectDocs) => DocumentNode;

// Resolve + print the VM document string for the proxy in `ctx`. Returns
// `undefined` when the resolved dialect doesn't carry the selected document
// (the surface diverges across snapshots — e.g. v0.12.0 dropped the legacy
// `*V2` / namespace docs, and older snapshots predate `workloadReports` /
// `destinationTypes`). `canRun` is invoked synchronously during render and
// treats an `undefined` query as "unsupported in this dialect", so degrading
// gracefully here is what keeps a version/dialect mismatch from throwing
// `print(undefined)` ("Invalid AST Node: undefined") inside React render and
// crashing the whole page.
export const vmQuery = (ctx: OperationContext, select: VmSelect): string | undefined => {
  const document = select(resolveDialect(dialectVersion(ctx)) as unknown as DialectDocs);
  return document ? print(document) : undefined;
};

// agents-api v0.12.0 replaced the `computePlatform { actions, instrumentationRules }`
// reads and the `*V2` / legacy mutations with the typed `actions` /
// `instrumentationRules` surface. Agents reporting an older schema can still be
// read (their `computePlatform` shape is mapped), but their write documents take
// a different input, so Central asks for an upgrade instead of sending a request
// the agent would reject.
export const isLegacyActionsAndRulesSchema = (ctx: OperationContext): boolean => !supportsTypedActionsAndRules(dialectVersion(ctx));

export const LEGACY_AGENT_UPGRADE_MESSAGE = 'This VM agent is too old to create, edit or delete actions and rules from Central. Upgrade the agent (it must report agents-api schema v0.12 or newer).';

export const assertAgentCanWriteActionsAndRules = (ctx: OperationContext | undefined): void => {
  if (ctx && isLegacyActionsAndRulesSchema(ctx)) throw new Error(LEGACY_AGENT_UPGRADE_MESSAGE);
};
