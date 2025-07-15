import type { DeleteUserPayload, Team } from '@/types';
import { Crud, StatusType } from '@odigos/ui-kit/types';
import { useNotificationStore } from '@odigos/ui-kit/store';
import { CREATE_TEAM, DELETE_TEAM, GET_TEAMS } from '@/graphql';
import { useQuery, useMutation, type ApolloError } from '@apollo/client';

interface CreateTeamVariables {
  name: string;
  description?: string;
  users: string[];
  computePlatforms: string[];
}

interface CreateTeamResponse {
  createTeam: {
    success: boolean;
  };
}

interface DeleteTeamVariables {
  teamID: string;
}

interface DeleteTeamResponse {
  deleteTeam: DeleteUserPayload;
}

export const useTeamsCRUD = () => {
  const { addNotification } = useNotificationStore();
  const onError = (error: ApolloError, crudType: Crud) => {
    addNotification({ type: StatusType.Error, title: error.name || crudType, message: error.cause?.message || error.message });
  };

  const { data, loading, error, refetch } = useQuery<{ teams: Team[] }>(GET_TEAMS);
  const [createTeamMutation, { loading: creating }] = useMutation<CreateTeamResponse, CreateTeamVariables>(CREATE_TEAM, { onError: (error) => onError(error, Crud.Create) });
  const [deleteTeamMutation, { loading: deleting }] = useMutation<DeleteTeamResponse, DeleteTeamVariables>(DELETE_TEAM, { onError: (error) => onError(error, Crud.Delete) });

  const createTeam = async (input: CreateTeamVariables): Promise<CreateTeamResponse | undefined | null> => {
    const response = await createTeamMutation({ variables: input });
    refetch();
    return response.data;
  };

  const deleteTeam = async (teamID: string): Promise<DeleteUserPayload | undefined | null> => {
    const { data } = await deleteTeamMutation({
      variables: { teamID },
      update: (cache) => {
        // Remove the deleted team from the cache
        cache.modify({
          fields: {
            teams(existingTeams = []) {
              return existingTeams.filter((team: Team) => team.id !== teamID);
            },
          },
        });
      },
    });

    if (data?.deleteTeam) {
      refetch();
      return data.deleteTeam;
    }

    return null;
  };

  return {
    teams: data?.teams || [],
    loading,
    error,
    refetch,
    createTeam,
    creating,
    deleteTeam,
    deleting,
  };
};
