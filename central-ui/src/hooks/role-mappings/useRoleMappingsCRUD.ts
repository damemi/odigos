import { Crud, StatusType } from '@odigos/ui-kit/types';
import { useNotificationStore } from '@odigos/ui-kit/store';
import { useQuery, useMutation } from '@apollo/client/react';
import { AuthProviderId, type GeneralResult, type GroupRoleMapping, type GroupRoleMappingInput } from '@/types';
import { CREATE_GROUP_ROLE_MAPPING, DELETE_GROUP_ROLE_MAPPING, GET_GROUP_ROLE_MAPPINGS, UPDATE_GROUP_ROLE_MAPPING } from '@/graphql';

export const useRoleMappingsCRUD = (providerId: AuthProviderId = AuthProviderId.AZURE) => {
  const { addNotification } = useNotificationStore();
  const notifyError = (error: { name?: string; message: string; cause?: unknown }, crudType: Crud) =>
    addNotification({ type: StatusType.Error, title: error.name || crudType, message: (error.cause instanceof Error ? error.cause.message : undefined) || error.message });

  const { data, loading, error, refetch } = useQuery<{ groupRoleMappings: GroupRoleMapping[] }, { providerId: AuthProviderId }>(GET_GROUP_ROLE_MAPPINGS, {
    variables: { providerId },
  });

  const [createMutation, { loading: creating }] = useMutation<{ createGroupRoleMapping: GeneralResult }, { input: GroupRoleMappingInput }>(CREATE_GROUP_ROLE_MAPPING);
  const [updateMutation, { loading: updating }] = useMutation<{ updateGroupRoleMapping: GeneralResult }, { id: string; input: GroupRoleMappingInput }>(UPDATE_GROUP_ROLE_MAPPING);
  const [deleteMutation, { loading: deleting }] = useMutation<{ deleteGroupRoleMapping: GeneralResult }, { providerId: AuthProviderId; id: string }>(DELETE_GROUP_ROLE_MAPPING);

  const createRoleMapping = async (input: GroupRoleMappingInput): Promise<GeneralResult | undefined> => {
    try {
      const { data } = await createMutation({ variables: { input } });
      refetch();
      return data?.createGroupRoleMapping;
    } catch (error) {
      notifyError(error instanceof Error ? error : { message: String(error) }, Crud.Create);
    }
  };

  const updateRoleMapping = async (id: string, input: GroupRoleMappingInput): Promise<GeneralResult | undefined> => {
    try {
      const { data } = await updateMutation({ variables: { id, input } });
      refetch();
      return data?.updateGroupRoleMapping;
    } catch (error) {
      notifyError(error instanceof Error ? error : { message: String(error) }, Crud.Update);
    }
  };

  const deleteRoleMapping = async (id: string): Promise<GeneralResult | undefined> => {
    try {
      const { data } = await deleteMutation({ variables: { providerId, id } });
      refetch();
      return data?.deleteGroupRoleMapping;
    } catch (error) {
      notifyError(error instanceof Error ? error : { message: String(error) }, Crud.Delete);
    }
  };

  return {
    roleMappings: data?.groupRoleMappings || [],
    loading,
    error,
    refetch,
    createRoleMapping,
    creating,
    updateRoleMapping,
    updating,
    deleteRoleMapping,
    deleting,
  };
};
