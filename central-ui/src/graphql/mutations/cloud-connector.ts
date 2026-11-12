import { gql } from '@apollo/client';

// Runs read-only reachability/permission checks for a prospective cloud
// connector via central-backend (which dispatches to connectors/testconnection).
// Credentials are validated in-memory only and never persisted. Drives the
// Create Cloud Connector drawer's validate step.
export const TEST_CLOUD_CONNECTOR_CONNECTION = gql`
  mutation TestCloudConnectorConnection($input: TestCloudConnectorConnectionInput!) {
    testCloudConnectorConnection(input: $input) {
      success
      accountId
      identity
      checks {
        name
        displayName
        passed
        message
        remediation
      }
    }
  }
`;

// Verifies the connection and, only if every check passes, creates the
// credentials Secret + OdigosCloudConnector CR in the central cluster. Returns
// the verification result so the drawer can render the shared success/failure UI.
export const CREATE_CLOUD_CONNECTOR = gql`
  mutation CreateCloudConnector($input: CreateCloudConnectorInput!) {
    createCloudConnector(input: $input) {
      created
      connectorId
      test {
        success
        accountId
        identity
        checks {
          name
          displayName
          passed
          message
          remediation
        }
      }
    }
  }
`;

export const UPDATE_CLOUD_CONNECTOR_COLLECTORS = gql`
  mutation UpdateCloudConnectorCollectors($connectorId: String!, $collectors: [CloudConnectorCollectorCapabilityInput!]!) {
    updateCloudConnectorCollectors(connectorId: $connectorId, collectors: $collectors) {
      success
      error
    }
  }
`;

// Deletes an OdigosCloudConnector CR (and credentials Secret) by the connection
// id used in computePlatforms (the CR UID). Also clears Redis proxy metadata.
export const DELETE_CLOUD_CONNECTOR = gql`
  mutation DeleteCloudConnector($connectorId: String!) {
    deleteCloudConnector(connectorId: $connectorId) {
      success
      error
    }
  }
`;
