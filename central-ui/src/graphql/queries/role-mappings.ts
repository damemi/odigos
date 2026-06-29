import { gql } from '@apollo/client';

export const GET_GROUP_ROLE_MAPPINGS = gql`
  query GetGroupRoleMappings($providerId: AuthProviderId!) {
    groupRoleMappings(providerId: $providerId) {
      id
      providerId
      claimName
      claimValue
      role
      displayName
    }
  }
`;
