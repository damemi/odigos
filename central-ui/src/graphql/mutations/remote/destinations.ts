import type { VersionedRemoteFetch } from '@/types';
import { PlatformType } from '@odigos/ui-kit/types';

export const CREATE_DESTINATION: VersionedRemoteFetch = {
  [PlatformType.K8s]: {
    'v1.11': `
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
      conditions {
        status
        type
        reason
        message

      }
    }
  }`,
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
      conditions {
        status
        type
        reason
        message

      }
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
