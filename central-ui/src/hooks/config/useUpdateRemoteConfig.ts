import { useCallback } from 'react';
import { useRemoteFetch } from '../proxy';
import { UPDATE_REMOTE_CONFIG } from '@/graphql';
import type { ComputePlatform, RemoteConfigInput } from '@/types';
import type { ApplyConfigurationsFunc } from '@odigos/ui-kit/types';

export const useUpdateRemoteConfig = (computePlatforms: ComputePlatform[]) => {
  const { fetchMulti } = useRemoteFetch<void, { config: RemoteConfigInput }>(UPDATE_REMOTE_CONFIG, computePlatforms);

  const updateRemoteConfigs: ApplyConfigurationsFunc = useCallback(
    async (formData, ids) => {
      const config: RemoteConfigInput = { rollout: formData.rollout };
      const res = await fetchMulti(ids, { config });
      if (res.error) return { error: res.error };
      return undefined;
    },
    [fetchMulti],
  );

  return {
    updateRemoteConfigs,
  };
};
