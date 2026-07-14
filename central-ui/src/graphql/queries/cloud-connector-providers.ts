import { gql } from '@apollo/client';

// Static cloud connector provider catalog served by central-backend (backed by
// the embedded connectors/spec YAML). Drives the Create Cloud Connector flow:
// the provider list (step 1) plus the connector/auth/feature config reused by
// the later config/validate steps.
export const GET_CLOUD_CONNECTOR_PROVIDERS = gql`
  query GetCloudConnectorProviders {
    cloudConnectorProviders {
      provider
      displayName
      connectorFields {
        name
        displayName
        componentType
        componentProps
        required
        secret
        tooltip
        initialValue
        envVar
      }
      auth {
        permissions
        methods {
          id
          displayName
          description
          baseMethods
          fields {
            name
            displayName
            componentType
            componentProps
            required
            secret
            tooltip
            initialValue
            envVar
          }
        }
      }
      features {
        key
        enabled
        displayName
        description
        permissions
        types {
          name
          displayName
          description
          permissions
          method
          implemented
        }
      }
    }
  }
`;
