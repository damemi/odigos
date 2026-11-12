import type { OperationContext } from '@odigos/ui-kit/contexts';

// What the connections list reports about one proxy: enough to pick its GraphQL bucket.
export interface ProxyVersion {
  platformType: OperationContext['platformType'];
  version: string;
  schemaVersion?: string;
}

// Context fields the adapter adds on top of the kit's `OperationContext`.
export type TargetableContext = OperationContext & {
  schemaVersion?: string;
  // Every proxy's own version, keyed by proxy ID. The kit's per-cluster calls only overlay
  // `proxyID` on the ambient context, so `version` there is still the selected proxy's.
  proxyVersions?: Record<string, ProxyVersion>;
};

/**
 * The context of the proxy an operation is addressed to (`proxyID`, else the first selected
 * connection), carrying that proxy's own platform and version. Undefined when the proxy's
 * version is unknown, so callers can refuse rather than send a document it may not support.
 */
export const targetProxyContext = <C extends TargetableContext>(ctx: C): C | undefined => {
  const proxyID = ctx.proxyID ?? ctx.connectionIds?.[0];
  if (!proxyID) return undefined;
  const known = ctx.proxyVersions?.[proxyID];
  if (!known) return undefined;
  return { ...ctx, proxyID, platformType: known.platformType, version: known.version, schemaVersion: known.schemaVersion };
};

/**
 * Index the connections list by proxy ID. The selected proxy's stored version seeds the map, so
 * the in-proxy pages work before the list has loaded; the live list wins once it has.
 */
export const buildProxyVersions = (
  connections: readonly { id: string; type: OperationContext['platformType']; odigosVersion?: string | null; graphqlSchemaVersion?: string | null }[],
  selected?: { proxyID?: string; platformType?: OperationContext['platformType']; odigosVersion?: string; graphqlSchemaVersion?: string },
): Record<string, ProxyVersion> => {
  const out: Record<string, ProxyVersion> = {};
  if (selected?.proxyID && selected.platformType && selected.odigosVersion) {
    out[selected.proxyID] = { platformType: selected.platformType, version: selected.odigosVersion, schemaVersion: selected.graphqlSchemaVersion };
  }
  for (const { id, type, odigosVersion, graphqlSchemaVersion } of connections) {
    if (!odigosVersion) continue;
    out[id] = { platformType: type, version: odigosVersion, schemaVersion: graphqlSchemaVersion ?? undefined };
  }
  return out;
};
