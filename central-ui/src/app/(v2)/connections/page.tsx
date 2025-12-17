'use client';

import React from 'react';
import { useNotificationStore } from '@odigos/ui-kit/store';
import { ComputePlatform, RemoteConfigInput } from '@/types';
import { GET_REMOTE_CONFIG, UPDATE_REMOTE_CONFIG } from '@/graphql';
import { type ConnectionRemoteConfig, StatusType } from '@odigos/ui-kit/types';
import { useComputePlatformsCRUD, useProxy, useProxyCRUD, useRemoteFetch } from '@/hooks';
import { ApplyConnectionsConfigurationsFunc, CentralConnections } from '@odigos/ui-kit/containers/v2';

export default function Page() {
  const { selectProxy } = useProxy();
  const { deleteProxy } = useProxyCRUD();
  const { addNotification } = useNotificationStore();
  const { computePlatforms, getComputePlatforms } = useComputePlatformsCRUD();

  const { fetch: getRemoteConfig } = useRemoteFetch<{ remoteConfig: ConnectionRemoteConfig }, void>(GET_REMOTE_CONFIG, computePlatforms);
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

  const applyConfigurations: ApplyConnectionsConfigurationsFunc = (ids, formData) => {
    return updateRemoteConfig(ids, {
      config: {
        rollout: {
          automaticRolloutDisabled: !formData.automaticRollout,
        },
      },
    });
  };

  return (
    <CentralConnections
      tableRowsMaxHeight={'calc(100vh - 180px)'}
      connections={computePlatforms.length ? computePlatforms : undefined}
      getConnections={getComputePlatforms}
      onClickConnection={onClickConnection}
      deleteConnection={deleteConnection}
      getConnectionRemoteConfig={async (id) => {
        const { data } = await getRemoteConfig(undefined, id);
        // TODO: support undefined in kit then remove the OR value
        return data?.remoteConfig || { rollout: { automaticRolloutDisabled: false } };
      }}
      applyConfigurations={applyConfigurations}
    />
  );
}
