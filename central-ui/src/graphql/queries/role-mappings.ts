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

// Every provider's mappings in one request; a provider that isn't connected has none.
export const GET_ALL_GROUP_ROLE_MAPPINGS = gql`
  query GetAllGroupRoleMappings {
    azure: groupRoleMappings(providerId: AZURE) {
      ...GroupRoleMappingFields
    }
    okta: groupRoleMappings(providerId: OKTA) {
      ...GroupRoleMappingFields
    }
    google: groupRoleMappings(providerId: GOOGLE) {
      ...GroupRoleMappingFields
    }
  }

  fragment GroupRoleMappingFields on GroupRoleMapping {
    id
    providerId
    claimName
    claimValue
    role
    displayName
  }
`;
