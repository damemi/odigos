import { useCallback } from 'react';
import { UPDATE_LOCAL_UI_CONFIG } from '@/graphql';
import { useRemoteFetch } from '../proxy';
import { useNotificationStore } from '@odigos/ui-kit/store';
import { Crud, StatusType, type LocalUiConfigInput } from '@odigos/ui-kit/types';

export const useUpdateLocalUiConfig = () => {
  const { addNotification } = useNotificationStore();
  const { fetch, loading } = useRemoteFetch<{ updateLocalUiConfig: boolean }, { config: LocalUiConfigInput }>(UPDATE_LOCAL_UI_CONFIG);

  const updateLocalUiConfig = useCallback(
    async (config: LocalUiConfigInput) => {
      const { data, error } = await fetch({ config });
      if (error) {
        addNotification({
          type: StatusType.Error,
          title: Crud.Update,
          message: error,
        });
        return;
      }
      if (data?.updateLocalUiConfig) {
        addNotification({
          type: StatusType.Success,
          title: Crud.Update,
          message: 'Local UI configuration updated successfully',
        });
      }
    },
    [fetch, addNotification],
  );

  return {
    updateLocalUiConfig,
    loading,
  };
};
