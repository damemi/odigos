import { gql } from '@apollo/client';

export const CREATE_TEAM = gql`
  mutation CreateTeam($input: WriteTeamInput!) {
    createTeam(input: $input) {
      success
      error
    }
  }
`;

export const UPDATE_TEAM = gql`
  mutation UpdateTeam($teamID: String!, $input: WriteTeamInput!) {
    updateTeam(teamID: $teamID, input: $input) {
      success
      error
    }
  }
`;

export const DELETE_TEAM = gql`
  mutation DeleteTeam($teamID: String!) {
    deleteTeam(teamID: $teamID) {
      success
      error
    }
  }
`;
