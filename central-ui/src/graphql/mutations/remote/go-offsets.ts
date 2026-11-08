import type { VersionedRemoteFetch } from '@/types';
import { PlatformType } from '@odigos/ui-kit/types';

// Compares (check) or installs (update) the Go offsets file the browser downloaded; both from odigos v1.38.
export const CHECK_GO_OFFSETS_UPDATES: VersionedRemoteFetch = {
  [PlatformType.K8s]: {
    'v1.38': `
  mutation CheckGoOffsetsUpdates($content: String!) {
    checkGoOffsetsUpdates(content: $content) {
      hasUpdates
      currentTimestamp
      proposedTimestamp
      mods {
        module
        isNew
        isRemoved
        minVersion
        maxVersion
        minorVersions {
          minorVersion
          isNew
          isRemoved
          versions {
            version
            isNew
            isRemoved
          }
        }
      }
    }
  }`,
  },
};

export const UPDATE_GO_OFFSETS: VersionedRemoteFetch = {
  [PlatformType.K8s]: {
    'v1.38': `
  mutation UpdateGoOffsets($content: String!) {
    updateGoOffsets(content: $content)
  }`,
  },
};
