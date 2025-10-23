import { useEffect } from 'react';
import { Crud, StatusType } from '@odigos/ui-kit/types';
import { ApolloError, useMutation, useQuery } from '@apollo/client';
import { GET_COMPUTE_PLATFORMS, UPDATE_COMPUTE_PLATFORM } from '@/graphql';
import { useNotificationStore, useSelectedStore } from '@odigos/ui-kit/store';
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

  const updateComputePlatform = async (id: string, input: ComputePlatformInput): Promise<GeneralResult | undefined> => {
    const { data } = await updateComputePlatformMutation({ variables: { id, input } });
    refetch();

    return data?.updateComputePlatform;
  };

  const { setSelectedConnection } = useSelectedStore();

  useEffect(() => {
    if (!selectedId || !data?.computePlatforms) return;

    const found = data.computePlatforms.find((cp) => cp.id === selectedId);
    if (found) setSelectedConnection(found);
  }, [selectedId, data?.computePlatforms]);

  return {
    loading,
    error,
    refetch,
    computePlatforms: data?.computePlatforms || [],
    updateComputePlatform,
    updating,
  };
};
