import { trimVersion } from '@odigos/ui-kit/functions';
import { type NotifyPayload } from '@odigos/ui-kit/store';
import { Role, type ComputePlatform, type VersionedRemoteFetch } from '@/types';
import { PlatformType, StatusType } from '@odigos/ui-kit/types';

// eslint-disable-next-line @typescript-eslint/no-explicit-any
export function stripNullFields<T>(obj: T): T {
  if (obj === null || obj === undefined) return obj;
  if (Array.isArray(obj)) return obj.map(stripNullFields) as T;
  if (typeof obj === 'object') {
    const result: Record<string, unknown> = {};
    for (const [key, value] of Object.entries(obj as Record<string, any>)) {
      if (value !== null && value !== undefined) {
        result[key] = stripNullFields(value);
      }
    }
    return result as T;
  }
  return obj;
}

export const getStatusForRole = (role: Role): StatusType => {
  switch (role) {
    case Role.VIEWER:
      return StatusType.Info;
    case Role.ADMIN:
      return StatusType.Error;
    case Role.MAINTAINER:
      return StatusType.Warning;
    case Role.OWNER:
      return StatusType.Default;
  }
};

export const getVersionedQuery = (query: VersionedRemoteFetch, platformType?: PlatformType, odigosVersion?: string, notify?: (notification: NotifyPayload) => void): string | undefined => {
  if (!platformType || !odigosVersion) return undefined;

  // do not reverse this sort, or it will directly affect the targetVersion calculation
  const supportedVersions = (Object.keys(query[platformType] || {}) as (keyof VersionedRemoteFetch[PlatformType])[]).sort((a, b) => trimVersion(b) - trimVersion(a));
  const minSupportedVersion = supportedVersions[supportedVersions.length - 1];
  const targetVersion = supportedVersions.find((supportedVersion) => trimVersion(odigosVersion) >= trimVersion(supportedVersion));

  if (!targetVersion) {
    // Extract any query string from the nested structure to parse the operation name for the error message.
    // Example: query = { k8s: { 'v1.16': 'query GetSources(...) {...}' }, vm: { 'v0.1': '...' } }
    //   -> firstPlatformQueries = { 'v1.16': 'query GetSources(...) {...}' }
    //   -> anyQueryString = 'query GetSources(...) {...}'
    const firstPlatformQueries = Object.values(query || {})[0] || {};
    const anyQueryString = (Object.values(firstPlatformQueries)[0] as string) || '';

    const [_, operationName] = anyQueryString.replaceAll('\n', ' ').trim().split(' ');
    const featureString = operationName ? `The "${operationName.split('(')[0]}"` : 'This';

    if (minSupportedVersion) {
      notify?.({
        type: StatusType.Error,
        title: 'Upgrade Required',
        message: `${featureString} feature is not supported for "${odigosVersion}", please upgrade the Proxy to "${minSupportedVersion}" or later.`,
      });
    } else {
      notify?.({
        type: StatusType.Error,
        title: 'Not Supported',
        message: `${featureString} feature is not supported for "${platformType.toUpperCase()}" platform.`,
      });
    }
  }

  return targetVersion ? query[platformType]?.[targetVersion] : undefined;
};

/**
 * Pick a fallback proxy ID for cross-cluster queries that need any working cluster.
 * Returns the first connection that is K8s-platform and has a successful (live) connection.
 *
 * Used by destination-info hooks (categories, auto-detected destinations, test connection)
 * when invoked from the central connections page where no proxy is selected.
 */
export const pickFallbackProxyID = (connections: ComputePlatform[] = []): string | undefined => {
  const found = connections.find((c) => c.type === PlatformType.K8s && c.status === StatusType.Success);
  return found?.id;
};
