import { useCallback, useState } from 'react';
import { useConfig } from '../config';
import { UPDATE_REMOTE_CONFIG } from '@/graphql';
import { Crud, StatusType } from '@odigos/ui-kit/types';
import { useNotificationStore } from '@odigos/ui-kit/store';
import { useMultiProxy, type MultiProxyResponse } from '../proxy';
import { DISPLAY_TITLES, FORM_ALERTS } from '@odigos/ui-kit/constants';
import type { RemoteConfigInput, UpdateRemoteConfigResponse } from '@/types';

interface UseRemoteConfig {
  loading: boolean;
  updateRemoteConfig: (proxyIDs: string[], config: RemoteConfigInput) => Promise<MultiProxyResponse<UpdateRemoteConfigResponse>>;
  setAutomaticRolloutDisabled: (proxyIDs: string[], disabled: boolean) => Promise<MultiProxyResponse<UpdateRemoteConfigResponse>>;
}

export const useRemoteConfig = (): UseRemoteConfig => {
  const { isReadonly } = useConfig();
  const { addNotification } = useNotificationStore();
  const [loading, setLoading] = useState(false);

  const { fetchAll: mutateConfig } = useMultiProxy<UpdateRemoteConfigResponse, { config: RemoteConfigInput }>(UPDATE_REMOTE_CONFIG);

  const updateRemoteConfig: UseRemoteConfig['updateRemoteConfig'] = useCallback(
    async (proxyIDs: string[], config: RemoteConfigInput) => {
      if (isReadonly) {
        addNotification({ type: StatusType.Warning, title: DISPLAY_TITLES.READONLY, message: FORM_ALERTS.READONLY_WARNING, hideFromHistory: true });
        return { results: [], allSucceeded: false, anySucceeded: false, successCount: 0, failureCount: 0 };
      }

      setLoading(true);
      try {
        const response = await mutateConfig(proxyIDs, { config });

        if (response.allSucceeded) {
          addNotification({ type: StatusType.Success, title: Crud.Update, message: `Remote configuration updated successfully on ${response.successCount} proxies` });
        } else if (response.anySucceeded) {
          addNotification({
            type: StatusType.Warning,
            title: Crud.Update,
            message: `Remote configuration updated on ${response.successCount}/${proxyIDs.length} proxies. ${response.failureCount} failed.`,
          });
        } else {
          addNotification({ type: StatusType.Error, title: Crud.Update, message: `Failed to update remote configuration on all ${proxyIDs.length} proxies` });
        }

        return response;
      } finally {
        setLoading(false);
      }
    },
    [isReadonly, mutateConfig, addNotification],
  );

  const setAutomaticRolloutDisabled: UseRemoteConfig['setAutomaticRolloutDisabled'] = useCallback(
    async (proxyIDs: string[], disabled: boolean) => {
      return updateRemoteConfig(proxyIDs, {
        rollout: {
          automaticRolloutDisabled: disabled,
        },
      });
    },
    [updateRemoteConfig],
  );

  return {
    loading,
    updateRemoteConfig,
    setAutomaticRolloutDisabled,
  };
};
