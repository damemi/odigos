'use client';

import React from 'react';
import { useOdigos } from '@odigos/ui-kit/contexts';
import { useNotificationStore } from '@odigos/ui-kit/store';
import { ComputePlatform, RemoteConfigInput } from '@/types';
import { GET_EFFECTIVE_CONFIG, UPDATE_REMOTE_CONFIG } from '@/graphql';
import { type EffectiveConfig, StatusType } from '@odigos/ui-kit/types';
import { useComputePlatformsCRUD, useProxy, useProxyCRUD, useRemoteFetch } from '@/hooks';
import { type ApplyConfigurationsFunc, CentralConnections } from '@odigos/ui-kit/containers/v2';

export default function Page() {
  const { isVm } = useOdigos();
  const { selectProxy } = useProxy();
  const { deleteProxy } = useProxyCRUD();
  const { addNotification } = useNotificationStore();
  const { computePlatforms, getComputePlatforms } = useComputePlatformsCRUD();

  const { fetch: fetchEffectiveConfig } = useRemoteFetch<{ effectiveConfig?: EffectiveConfig }, void>(GET_EFFECTIVE_CONFIG, computePlatforms);
  const { fetchMulti: updateRemoteConfig } = useRemoteFetch<void, { config: RemoteConfigInput }>(UPDATE_REMOTE_CONFIG, computePlatforms);

  const onClickConnection = ({ id, status }: ComputePlatform) => {
    if (status === StatusType.Success) {
      selectProxy(id);
    } else {
      addNotification({
        type: StatusType.Warning,
        title: 'Connection lost',
        message: 'Cannot connect to the platform',
        hideFromHistory: true,
      });
    }
  };

  const deleteConnection = async (id: string) => {
    await deleteProxy(id);
    return Promise.resolve();
  };

  const getEffectiveConfig = async (proxyId: string) => {
    const { data } = await fetchEffectiveConfig(undefined, proxyId);
    return data?.effectiveConfig;
  };

  const applyConfigurations: ApplyConfigurationsFunc = (ids, formData) => {
    return updateRemoteConfig(ids, { config: formData });
  };

  return (
    <CentralConnections
      tableRowsMaxHeight={'calc(100vh - 180px)'}
      connections={computePlatforms.length ? computePlatforms : undefined}
      getConnections={getComputePlatforms}
      onClickConnection={onClickConnection}
      deleteConnection={deleteConnection}
      getEffectiveConfig={isVm ? undefined : getEffectiveConfig}
      applyConfigurations={isVm ? undefined : applyConfigurations}
    />
  );
}
