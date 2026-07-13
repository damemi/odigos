import { useEffect } from 'react';
import { Crud, StatusType } from '@odigos/ui-kit/types';
import { useNotificationStore } from '@odigos/ui-kit/store';
import { useMutation, useQuery } from '@apollo/client/react';
import { GET_COMPUTE_PLATFORMS, UPDATE_COMPUTE_PLATFORM } from '@/graphql';
import type { ComputePlatform, ComputePlatformInput, GeneralResult } from '@/types';

export const useComputePlatformsCRUD = () => {
  const { addNotification } = useNotificationStore();

  const notifyError = (error: { name?: string; message: string; cause?: unknown }, crudType: Crud) =>
    addNotification({ type: StatusType.Error, title: error.name || crudType, message: (error.cause instanceof Error ? error.cause.message : undefined) || error.message });

  const { refetch, loading, error, data } = useQuery<{ computePlatforms?: ComputePlatform[] }>(GET_COMPUTE_PLATFORMS);
  const [update, { loading: updating }] = useMutation<{ updateComputePlatform: GeneralResult }, { id: string; input: ComputePlatformInput }>(UPDATE_COMPUTE_PLATFORM);

  useEffect(() => {
    if (error) notifyError(error, Crud.Read);
  }, [error]);

  const getComputePlatforms = async () => {
    const { data } = await refetch();
    return data?.computePlatforms || [];
  };
  const updateComputePlatform = async (id: string, input: ComputePlatformInput): Promise<GeneralResult | undefined> => {
    try {
      const { data } = await update({ variables: { id, input } });
      refetch();
      return data?.updateComputePlatform;
    } catch (err) {
      notifyError(err instanceof Error ? err : { message: String(err) }, Crud.Update);
    }
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
