import type { VersionedRemoteFetch } from '@/types';
import { PlatformType } from '@odigos/ui-kit/types';

// The Instrumentation Agents page: the agents a cluster can inject and its Go offsets. Both joined the schema in
// odigos v1.38; older proxies have no bucket, so the kit reports the page's data as unsupported.
export const GET_INSTRUMENTATION_AGENTS: VersionedRemoteFetch = {
  [PlatformType.K8s]: {
    'v1.38': `
  query GetInstrumentationAgents {
    instrumentationAgents {
      language
      distroName
      distroDisplayName
      description
      runtimeEnvironment
      supportedRuntimeVersions
      sources
      isDefault
    }
  }`,
  },
};

export const GET_GO_OFFSETS: VersionedRemoteFetch = {
  [PlatformType.K8s]: {
    'v1.38': `
  query GetGoOffsets {
    goOffsets {
      installed
      timestamp
      mods {
        module
        minVersion
        maxVersion
        minorVersions {
          minorVersion
          versions
        }
      }
    }
  }`,
  },
};
