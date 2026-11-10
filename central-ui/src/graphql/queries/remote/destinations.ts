import type { VersionedRemoteFetch } from '@/types';
import { PlatformType } from '@odigos/ui-kit/types';

// `profiles` joined `ExportedSignals` / `SupportedSignals` in odigos v1.26.
// Older proxies reject the selection, so it is only requested from that bucket
// on. Without it the kit treats every destination as not supporting profiles:
// OTLP hides the Profiles checkbox, a profiles-only type (Pyroscope) reports no
// supported signals at all, and an edit re-saves `profiles: false`.
const EXPORTED_SIGNALS_V111 = `logs metrics traces`;
const EXPORTED_SIGNALS_V126 = `${EXPORTED_SIGNALS_V111} profiles`;
const SUPPORTED_SIGNALS_V111 = `logs { supported } metrics { supported } traces { supported }`;
const SUPPORTED_SIGNALS_V126 = `${SUPPORTED_SIGNALS_V111} profiles { supported }`;

const getDestinationsQuery = (exportedSignals: string, supportedSignals: string) => `
  query GetDestinations {
    computePlatform {
      destinations {
        id
        name
        disabled
        dataStreamNames
        fields
        exportedSignals { ${exportedSignals} }
        destinationType {
          type
          imageUrl
          displayName
          supportedSignals { ${supportedSignals} }
        }
        conditions {
          status
          type
          reason
          message

        }
      }
    }
  }`;

export const GET_DESTINATIONS: VersionedRemoteFetch = {
  [PlatformType.K8s]: {
    'v1.11': getDestinationsQuery(EXPORTED_SIGNALS_V111, SUPPORTED_SIGNALS_V111),
    'v1.26': getDestinationsQuery(EXPORTED_SIGNALS_V126, SUPPORTED_SIGNALS_V126),
  },
  // VM proxies resolve destinations from the versioned vmagent dialect registry
  // (see the VmSpec on GET_DESTINATIONS in odigos-api-adapter).
};

const getDestinationCategoriesQuery = (supportedSignals: string) => `
  query GetDestinationCategories {
    destinationCategories {
      categories {
        name
        description
        items {
          type
          testConnectionSupported
          displayName
          imageUrl
          supportedSignals { ${supportedSignals} }
          fields {
            name
            displayName
            componentType
            componentProperties
            secret
            initialValue
            renderCondition
            hideFromReadData
            customReadDataLabels {
              condition
              title
              value
            }
          }
        }
      }
    }
  }`;

export const GET_DESTINATION_CATEGORIES: VersionedRemoteFetch = {
  [PlatformType.K8s]: {
    'v1.11': getDestinationCategoriesQuery(SUPPORTED_SIGNALS_V111),
    'v1.26': getDestinationCategoriesQuery(SUPPORTED_SIGNALS_V126),
  },
  // VM proxies resolve the destination catalog from the versioned vmagent
  // `destinationTypes` dialect document (see the VmSpec on
  // GET_DESTINATION_CATEGORIES in odigos-api-adapter).
};

export const GET_POTENTIAL_DESTINATIONS: VersionedRemoteFetch = {
  [PlatformType.K8s]: {
    'v1.11': `
  query GetPotentialDestinations {
    potentialDestinations {
      type
      fields
    }
  }`,
  },
};
