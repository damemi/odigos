import type { VersionedRemoteFetch } from '@/types';
import { PlatformType } from '@odigos/ui-kit/types';

// `profiles` joined `ExportedSignals` / `SupportedSignals` in odigos v1.26
// (see GET_DESTINATIONS). The created destination lands in the kit's entity
// store as-is, so it must carry `profiles` or a later edit re-saves it as false.
const EXPORTED_SIGNALS_V111 = `logs metrics traces`;
const EXPORTED_SIGNALS_V126 = `${EXPORTED_SIGNALS_V111} profiles`;
const SUPPORTED_SIGNALS_V111 = `logs { supported } metrics { supported } traces { supported }`;
const SUPPORTED_SIGNALS_V126 = `${SUPPORTED_SIGNALS_V111} profiles { supported }`;

const getCreateDestinationMutation = (exportedSignals: string, supportedSignals: string) => `
  mutation CreateNewDestination($destination: DestinationInput!) {
    createNewDestination(destination: $destination) {
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
  }`;

export const CREATE_DESTINATION: VersionedRemoteFetch = {
  [PlatformType.K8s]: {
    'v1.11': getCreateDestinationMutation(EXPORTED_SIGNALS_V111, SUPPORTED_SIGNALS_V111),
    'v1.26': getCreateDestinationMutation(EXPORTED_SIGNALS_V126, SUPPORTED_SIGNALS_V126),
  },
  [PlatformType.Vm]: {
    'v0.1': `
  mutation CreateNewDestination($destination: DestinationInput!) {
    createNewDestination(destination: $destination) {
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
      conditions
    }
  }`,
  },
};

export const UPDATE_DESTINATION: VersionedRemoteFetch = {
  [PlatformType.K8s]: {
    'v1.11': `
  mutation UpdateDestination($id: ID!, $destination: DestinationInput!) {
    updateDestination(id: $id, destination: $destination) {
      id
    }
  }`,
  },
  [PlatformType.Vm]: {
    'v0.1': `
  mutation UpdateDestination($id: ID!, $destination: DestinationInput!) {
    updateDestination(id: $id, destination: $destination) {
      id
    }
  }`,
  },
};

export const DELETE_DESTINATION: VersionedRemoteFetch = {
  [PlatformType.K8s]: {
    'v1.11': `
  mutation DeleteDestination($id: ID!, $currentStreamName: String!) {
    deleteDestination(id: $id, currentStreamName: $currentStreamName)
  }`,
  },
  [PlatformType.Vm]: {
    'v0.1': `
  mutation DeleteDestination($id: ID!, $currentStreamName: String!) {
    deleteDestination(id: $id, currentStreamName: $currentStreamName)
  }`,
  },
};

export const TEST_DESTINATION_CONNECTION: VersionedRemoteFetch = {
  [PlatformType.K8s]: {
    'v1.11': `
  mutation TestConnection($destination: DestinationInput!) {
    testConnectionForDestination(destination: $destination) {
      succeeded
      statusCode
      destinationType
      message
      reason
    }
  }`,
  },
};
