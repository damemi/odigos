import { Crud, StatusType } from '@odigos/ui-kit/types';
import { useNotificationStore } from '@odigos/ui-kit/store';
import { AuthProviderId, type GeneralResult, type GroupRoleMapping, type GroupRoleMappingInput } from '@/types';
import { CREATE_GROUP_ROLE_MAPPING, DELETE_GROUP_ROLE_MAPPING, GET_GROUP_ROLE_MAPPINGS, UPDATE_GROUP_ROLE_MAPPING } from '@/graphql';
import { useQuery, useMutation, type ApolloError } from '@apollo/client';

export const useRoleMappingsCRUD = (providerId: AuthProviderId = AuthProviderId.AZURE) => {
  const { addNotification } = useNotificationStore();
  const onError = (error: ApolloError, crudType: Crud) => addNotification({ type: StatusType.Error, title: error.name || crudType, message: error.cause?.message || error.message });

  const { data, loading, error, refetch } = useQuery<{ groupRoleMappings: GroupRoleMapping[] }, { providerId: AuthProviderId }>(GET_GROUP_ROLE_MAPPINGS, {
    variables: { providerId },
  });

  const [createMutation, { loading: creating }] = useMutation<{ createGroupRoleMapping: GeneralResult }, { input: GroupRoleMappingInput }>(CREATE_GROUP_ROLE_MAPPING, {
    onError: (error) => onError(error, Crud.Create),
  });
  const [updateMutation, { loading: updating }] = useMutation<{ updateGroupRoleMapping: GeneralResult }, { id: string; input: GroupRoleMappingInput }>(UPDATE_GROUP_ROLE_MAPPING, {
    onError: (error) => onError(error, Crud.Update),
  });
  const [deleteMutation, { loading: deleting }] = useMutation<{ deleteGroupRoleMapping: GeneralResult }, { providerId: AuthProviderId; id: string }>(DELETE_GROUP_ROLE_MAPPING, {
    onError: (error) => onError(error, Crud.Delete),
  });

  const createRoleMapping = async (input: GroupRoleMappingInput): Promise<GeneralResult | undefined> => {
    const { data } = await createMutation({ variables: { input } });
    refetch();
    return data?.createGroupRoleMapping;
  };

  const updateRoleMapping = async (id: string, input: GroupRoleMappingInput): Promise<GeneralResult | undefined> => {
    const { data } = await updateMutation({ variables: { id, input } });
    refetch();
    return data?.updateGroupRoleMapping;
  };

  const deleteRoleMapping = async (id: string): Promise<GeneralResult | undefined> => {
    const { data } = await deleteMutation({ variables: { providerId, id } });
    refetch();
    return data?.deleteGroupRoleMapping;
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
