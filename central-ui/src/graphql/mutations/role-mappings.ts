import { gql } from '@apollo/client';

export const CREATE_GROUP_ROLE_MAPPING = gql`
  mutation CreateGroupRoleMapping($input: WriteGroupRoleMappingInput!) {
    createGroupRoleMapping(input: $input) {
      success
      error
    }
  }
`;

export const UPDATE_GROUP_ROLE_MAPPING = gql`
  mutation UpdateGroupRoleMapping($id: String!, $input: WriteGroupRoleMappingInput!) {
    updateGroupRoleMapping(id: $id, input: $input) {
      success
      error
    }
  }
`;

export const DELETE_GROUP_ROLE_MAPPING = gql`
  mutation DeleteGroupRoleMapping($providerId: AuthProviderId!, $id: String!) {
    deleteGroupRoleMapping(providerId: $providerId, id: $id) {
      success
      error
    }
  }
`;
