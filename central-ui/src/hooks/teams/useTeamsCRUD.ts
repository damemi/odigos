import { Crud, StatusType } from '@odigos/ui-kit/types';
import { useNotificationStore } from '@odigos/ui-kit/store';
import type { GeneralResult, Team, WriteTeamInput } from '@/types';
import { CREATE_TEAM, DELETE_TEAM, GET_TEAMS, UPDATE_TEAM } from '@/graphql';
import { useQuery, useMutation, type ApolloError } from '@apollo/client';

export const useTeamsCRUD = () => {
  const { addNotification } = useNotificationStore();
  const onError = (error: ApolloError, crudType: Crud) => addNotification({ type: StatusType.Error, title: error.name || crudType, message: error.cause?.message || error.message });

  const { data, loading, error, refetch } = useQuery<{ teams: Team[] }>(GET_TEAMS);
  const [createTeamMutation, { loading: creating }] = useMutation<{ createTeam: GeneralResult }, { input: WriteTeamInput }>(CREATE_TEAM, {
    onError: (error) => onError(error, Crud.Create),
  });
  const [updateTeamMutation, { loading: updating }] = useMutation<{ updateTeam: GeneralResult }, { teamID: string; input: WriteTeamInput }>(UPDATE_TEAM, {
    onError: (error) => onError(error, Crud.Update),
  });
  const [deleteTeamMutation, { loading: deleting }] = useMutation<{ deleteTeam: GeneralResult }, { teamID: string }>(DELETE_TEAM, {
    onError: (error) => onError(error, Crud.Delete),
  });

  const createTeam = async (input: WriteTeamInput): Promise<GeneralResult | undefined> => {
    const { data } = await createTeamMutation({ variables: { input } });
    refetch();

    return data?.createTeam;
  };

  const updateTeam = async (teamID: string, input: WriteTeamInput): Promise<GeneralResult | undefined> => {
    const { data } = await updateTeamMutation({ variables: { teamID, input } });
    refetch();

    return data?.updateTeam;
  };

  const deleteTeam = async (teamID: string): Promise<GeneralResult | undefined> => {
    const { data } = await deleteTeamMutation({
      variables: { teamID },
      update: (cache) => {
        // Remove the deleted team from the cache
        cache.modify({
          fields: {
            teams: (existingTeams = []) => existingTeams.filter((team: Team) => team.id !== teamID),
          },
        });
      },
    });
    refetch();

    return data?.deleteTeam;
  };

  return {
    teams: data?.teams || [],
    loading,
    error,
    refetch,
    createTeam,
    creating,
    updateTeam,
    updating,
    deleteTeam,
    deleting,
  };
};
