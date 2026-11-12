import { gql } from '@apollo/client';

// Revoke is part of the current agents-api schema but has not reached the
// published frozen dialect yet. Keep this document local until publication.
export const REVOKE_MANAGED_COLLECTOR_CREDENTIAL = gql`
  mutation RevokeManagedCollectorCredential($input: RevokeManagedCollectorCredentialInput!) {
    revokeManagedCollectorCredential(input: $input) {
      region
      credentialId
      revoked
    }
  }
`;
