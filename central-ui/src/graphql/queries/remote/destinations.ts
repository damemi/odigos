import type { VersionedRemoteFetch } from '@/types';
import { PlatformType } from '@odigos/ui-kit/types';

export const GET_DESTINATIONS: VersionedRemoteFetch = {
  [PlatformType.K8s]: {
    'v1.11': `
  query GetDestinations {
    computePlatform {
      destinations {
        id
        name
        disabled
        dataStreamNames
        fields
        exportedSignals {
          logs
          metrics
          traces
        }
        destinationType {
          type
          imageUrl
          displayName
          supportedSignals {
            logs {
              supported
            }
            metrics {
              supported
            }
            traces {
              supported
            }
          }
        }
        conditions {
          status
          type
          reason
          message
          lastTransitionTime
        }
      }
    }
  }`,
  },
  [PlatformType.Vm]: {
    'v0.1': `
  query GetDestinations {
    computePlatform {
      destinations {
        id
        name
        disabled
        dataStreamNames
        fields
        exportedSignals {
          logs
          metrics
          traces
        }
        destinationType {
          type
          imageUrl
          displayName
          supportedSignals {
            logs {
              supported
            }
            metrics {
              supported
            }
            traces {
              supported
            }
          }
        }
        conditions {
          status
          type
          reason
          message
          lastTransitionTime
        }
      }
    }
  }`,
  },
};

export const GET_DESTINATION_CATEGORIES: VersionedRemoteFetch = {
  [PlatformType.K8s]: {
    'v1.11': `
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
          supportedSignals {
            logs {
              supported
            }
            metrics {
              supported
            }
            traces {
              supported
            }
          }
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
  }`,
  },
  [PlatformType.Vm]: {
    'v0.1': `
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
          supportedSignals {
            logs {
              supported
            }
            metrics {
              supported
            }
            traces {
              supported
            }
          }
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
  }`,
  },
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
