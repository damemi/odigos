import { useCallback } from 'react';
import { useRemoteFetch } from '../proxy';
import { useNotificationStore } from '@odigos/ui-kit/store';
import { Crud, StatusType, type LocalUiConfigInput } from '@odigos/ui-kit/types';
import { UPDATE_LOCAL_UI_CONFIG, RESET_LOCAL_UI_CONFIG_TO_FACTORY_DEFAULTS } from '@/graphql';

export const useUpdateLocalUiConfig = () => {
  const { addNotification } = useNotificationStore();
  const { fetch, loading } = useRemoteFetch<{ updateLocalUiConfig: boolean }, { config: LocalUiConfigInput }>(UPDATE_LOCAL_UI_CONFIG);
  const { fetch: fetchReset, loading: resetLoading } = useRemoteFetch<{ resetLocalUiConfigToFactoryDefaults: boolean }>(RESET_LOCAL_UI_CONFIG_TO_FACTORY_DEFAULTS);

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

  const resetLocalUiConfigToFactoryDefaults = useCallback(async () => {
    const { data, error } = await fetchReset({});
    if (error) {
      addNotification({
        type: StatusType.Error,
        title: Crud.Update,
        message: error,
      });
      return;
    }
    if (data?.resetLocalUiConfigToFactoryDefaults) {
      addNotification({
        type: StatusType.Success,
        title: Crud.Update,
        message: 'Local UI configuration reset to factory defaults',
      });
    }
  }, [fetchReset, addNotification]);

  return {
    updateLocalUiConfig,
    resetLocalUiConfigToFactoryDefaults,
    loading: loading || resetLoading,
  };
};
