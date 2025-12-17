import { StatusType } from '@odigos/ui-kit/types';
import { trimVersion } from '@odigos/ui-kit/functions';
import { type NotifyPayload } from '@odigos/ui-kit/store';
import { Role, type VersionedRemoteFetch } from '@/types';

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

type GetVersionedQuery = (query: VersionedRemoteFetch, odigosVersion: string, notify?: (notification: NotifyPayload) => void) => string | undefined;

export const getVersionedQuery: GetVersionedQuery = (query, odigosVersion, notify) => {
  // do not reverse this sort, or it will directly affect the targetVersion calculation
  const supportedVersions = (Object.keys(query) as (keyof VersionedRemoteFetch)[]).sort((a, b) => trimVersion(a) - trimVersion(b));
  const targetVersion = supportedVersions.find((supportedVersion) => trimVersion(odigosVersion) >= trimVersion(supportedVersion));

  if (!targetVersion) {
    notify?.({
      type: StatusType.Error,
      title: 'Upgrade Required',
      message: `To use this feature, upgrade Proxy to Odigos ${supportedVersions[0]} or later. Current version: ${odigosVersion}.`,
    });

    return undefined;
  }
  return query[targetVersion];
};
