import { gql } from '@apollo/client';

export const UPDATE_COMPUTE_PLATFORM = gql`
  mutation UpdateComputePlatform($id: String!, $input: WriteComputePlatformInput!) {
    updateComputePlatform(id: $id, input: $input) {
      success
      error
    }
  }
`;
