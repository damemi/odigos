import { gql } from '@apollo/client';

export const CREATE_TEAM = gql`
  mutation CreateTeam($name: String!, $description: String, $users: [String]!, $computePlatforms: [String]!) {
    createTeam(name: $name, description: $description, users: $users, computePlatforms: $computePlatforms) {
      success
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
