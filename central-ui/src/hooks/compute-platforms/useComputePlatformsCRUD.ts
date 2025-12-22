import { Crud, StatusType } from '@odigos/ui-kit/types';
import { useNotificationStore } from '@odigos/ui-kit/store';
import { ApolloError, useMutation, useQuery } from '@apollo/client';
import { GET_COMPUTE_PLATFORMS, UPDATE_COMPUTE_PLATFORM } from '@/graphql';
import type { ComputePlatform, ComputePlatformInput, GeneralResult } from '@/types';

export const useComputePlatformsCRUD = () => {
  const { addNotification } = useNotificationStore();

  const onError = (error: ApolloError, crudType: Crud) => addNotification({ type: StatusType.Error, title: error.name || crudType, message: error.cause?.message || error.message });

  const { refetch, loading, error, data } = useQuery<{ computePlatforms?: ComputePlatform[] }>(GET_COMPUTE_PLATFORMS, {
    onError: (error) => onError(error, Crud.Read),
  });
  const [update, { loading: updating }] = useMutation<{ updateComputePlatform: GeneralResult }, { id: string; input: ComputePlatformInput }>(UPDATE_COMPUTE_PLATFORM, {
    onError: (error) => onError(error, Crud.Update),
  });

  const getComputePlatforms = async () => {
    const { data } = await refetch();
    return data?.computePlatforms || [];
  };
  const updateComputePlatform = async (id: string, input: ComputePlatformInput): Promise<GeneralResult | undefined> => {
    const { data } = await update({ variables: { id, input } });
    refetch();
    return data?.updateComputePlatform;
  };

  return {
    getComputePlatforms,
    computePlatforms: data?.computePlatforms || [],
    loading,
    error,

    updateComputePlatform,
    updating,
  };
};
