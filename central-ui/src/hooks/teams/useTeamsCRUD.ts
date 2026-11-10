import { Crud, StatusType } from '@odigos/ui-kit/types';
import { useNotificationStore } from '@odigos/ui-kit/store';
import { useQuery, useMutation } from '@apollo/client/react';
import type { GeneralResult, Team, TeamInput } from '@/types';
import { CREATE_TEAM, DELETE_TEAM, GET_TEAMS, UPDATE_TEAM } from '@/graphql';

export const useTeamsCRUD = () => {
  const { addNotification } = useNotificationStore();
  const notifyError = (error: { name?: string; message: string; cause?: unknown }, crudType: Crud) =>
    addNotification({ type: StatusType.Error, title: error.name || crudType, message: (error.cause instanceof Error ? error.cause.message : undefined) || error.message });

  const { data, loading, error, refetch } = useQuery<{ teams: Team[] }>(GET_TEAMS);
  const [createTeamMutation, { loading: creating }] = useMutation<{ createTeam: GeneralResult }, { input: TeamInput }>(CREATE_TEAM);
  const [updateTeamMutation, { loading: updating }] = useMutation<{ updateTeam: GeneralResult }, { teamID: string; input: TeamInput }>(UPDATE_TEAM);
  const [deleteTeamMutation, { loading: deleting }] = useMutation<{ deleteTeam: GeneralResult }, { teamID: string }>(DELETE_TEAM);

  const createTeam = async (input: TeamInput): Promise<GeneralResult | undefined> => {
    try {
      const { data } = await createTeamMutation({ variables: { input } });
      refetch();
      return data?.createTeam;
    } catch (error) {
      notifyError(error instanceof Error ? error : { message: String(error) }, Crud.Create);
    }
  };

  const updateTeam = async (teamID: string, input: TeamInput): Promise<GeneralResult | undefined> => {
    try {
      const { data } = await updateTeamMutation({ variables: { teamID, input } });
      refetch();
      return data?.updateTeam;
    } catch (error) {
      notifyError(error instanceof Error ? error : { message: String(error) }, Crud.Update);
    }
  };

  const deleteTeam = async (teamID: string): Promise<GeneralResult | undefined> => {
    try {
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
    } catch (error) {
      notifyError(error instanceof Error ? error : { message: String(error) }, Crud.Delete);
    }
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
