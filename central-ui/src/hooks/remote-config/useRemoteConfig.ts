import { useCallback, useEffect, useState } from 'react';
import { useProxy, useProxyID } from '../proxy';
import { useConfig } from '../config';
import { GET_REMOTE_CONFIG, UPDATE_REMOTE_CONFIG } from '@/graphql';
import { useNotificationStore } from '@odigos/ui-kit/store';
import { DISPLAY_TITLES, FORM_ALERTS } from '@odigos/ui-kit/constants';
import { Crud, StatusType } from '@odigos/ui-kit/types';
import type { RemoteConfig, RemoteConfigInput, GetRemoteConfigResponse, UpdateRemoteConfigResponse } from '@/types';

interface UseRemoteConfig {
  remoteConfig: RemoteConfig | null;
  loading: boolean;
  fetchRemoteConfig: () => Promise<void>;
  updateRemoteConfig: (config: RemoteConfigInput) => Promise<void>;
  // Convenience getters for common fields
  isAutomaticRolloutDisabled: boolean | null;
  setAutomaticRolloutDisabled: (disabled: boolean) => Promise<void>;
}

export const useRemoteConfig = (): UseRemoteConfig => {
  const { proxyID } = useProxyID();
  const { isReadonly } = useConfig();
  const { addNotification } = useNotificationStore();
  const [remoteConfig, setRemoteConfig] = useState<RemoteConfig | null>(null);
  const [loading, setLoading] = useState(false);

  const { fetch: queryConfig } = useProxy<GetRemoteConfigResponse>(GET_REMOTE_CONFIG);
  const { fetch: mutateConfig } = useProxy<UpdateRemoteConfigResponse, { config: RemoteConfigInput }>(UPDATE_REMOTE_CONFIG);

  const fetchRemoteConfig: UseRemoteConfig['fetchRemoteConfig'] = useCallback(async () => {
    if (!proxyID) return;

    setLoading(true);
    try {
      const { data } = await queryConfig();
      if (data?.remoteConfig) {
        setRemoteConfig(data.remoteConfig);
      } else {
        // If no remote config exists, set default empty config
        setRemoteConfig({ rollout: null });
      }
    } finally {
      setLoading(false);
    }
  }, [proxyID, queryConfig]);

  const updateRemoteConfig: UseRemoteConfig['updateRemoteConfig'] = useCallback(
    async (config: RemoteConfigInput) => {
      if (!proxyID) return;

      if (isReadonly) {
        addNotification({ type: StatusType.Warning, title: DISPLAY_TITLES.READONLY, message: FORM_ALERTS.READONLY_WARNING, hideFromHistory: true });
        return;
      }

      setLoading(true);
      try {
        const { data } = await mutateConfig({ config });
        if (data?.updateRemoteConfig) {
          setRemoteConfig(data.updateRemoteConfig);
          addNotification({ type: StatusType.Success, title: Crud.Update, message: 'Remote configuration updated successfully' });
        }
      } finally {
        setLoading(false);
      }
    },
    [proxyID, isReadonly, mutateConfig, addNotification],
  );

  // Convenience method to update just the automaticRolloutDisabled field
  const setAutomaticRolloutDisabled: UseRemoteConfig['setAutomaticRolloutDisabled'] = useCallback(
    async (disabled: boolean) => {
      await updateRemoteConfig({
        rollout: {
          automaticRolloutDisabled: disabled,
        },
      });
    },
    [updateRemoteConfig],
  );

  // Fetch config when proxyID becomes available
  useEffect(() => {
    if (proxyID) {
      fetchRemoteConfig();
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [proxyID]);

  // Convenience getter for automaticRolloutDisabled
  const isAutomaticRolloutDisabled = remoteConfig?.rollout?.automaticRolloutDisabled ?? null;

  return {
    remoteConfig,
    loading,
    fetchRemoteConfig,
    updateRemoteConfig,
    isAutomaticRolloutDisabled,
    setAutomaticRolloutDisabled,
  };
};
