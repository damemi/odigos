import { Crud, StatusType } from '@odigos/ui-kit/types';
import { useNotificationStore } from '@odigos/ui-kit/store';
import { ApolloError, useMutation, useQuery } from '@apollo/client';
import { GET_COMPUTE_PLATFORMS, UPDATE_COMPUTE_PLATFORM } from '@/graphql';
import type { ComputePlatform, ComputePlatformInput, GeneralResult } from '@/types';

export const useComputePlatformsCRUD = (selectedId?: string) => {
  const { addNotification } = useNotificationStore();
  const onError = (error: ApolloError, crudType: Crud) => addNotification({ type: StatusType.Error, title: error.name || crudType, message: error.cause?.message || error.message });

  const { refetch, loading, error, data } = useQuery<{ computePlatforms?: ComputePlatform[] }>(GET_COMPUTE_PLATFORMS, {
    onError: (error) => onError(error, Crud.Read),
  });
  const [updateComputePlatformMutation, { loading: updating }] = useMutation<{ updateComputePlatform: GeneralResult }, { id: string; input: ComputePlatformInput }>(UPDATE_COMPUTE_PLATFORM, {
    onError: (error) => onError(error, Crud.Update),
  });

  const getCurrentComputePlatform = (id: string | undefined) => {
    if (!id || !data?.computePlatforms) return undefined;
    return data.computePlatforms.find((cp) => cp.id === id);
  };

  const updateComputePlatform = async (id: string, input: ComputePlatformInput): Promise<GeneralResult | undefined> => {
    const { data } = await updateComputePlatformMutation({ variables: { id, input } });
    refetch();

    return data?.updateComputePlatform;
  };

  return {
    loading,
    error,
    refetch,
    computePlatforms: data?.computePlatforms || [],
    currentComputePlatform: getCurrentComputePlatform(selectedId),
    updateComputePlatform,
    updating,
  };
};
