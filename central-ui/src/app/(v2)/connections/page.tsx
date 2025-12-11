'use client';

import React from 'react';
import { UPDATE_REMOTE_CONFIG } from '@/graphql';
import { StatusType } from '@odigos/ui-kit/types';
import { ComputePlatform, RemoteConfigInput } from '@/types';
import { useNotificationStore, useSetupStore } from '@odigos/ui-kit/store';
import { useComputePlatformsCRUD, useMultiProxy, useProxyCRUD, useProxyID } from '@/hooks';
import { ApplyConnectionsConfigurationsFunc, CentralConnections } from '@odigos/ui-kit/containers/v2';

export default function Page() {
  const { clearStore } = useSetupStore();
  const { selectProxyID } = useProxyID();
  const { deleteProxy } = useProxyCRUD();
  const { addNotification } = useNotificationStore();
  const { computePlatforms, getComputePlatforms } = useComputePlatformsCRUD();
  const { fetchAll: updateRemoteConfig } = useMultiProxy<void, { config: RemoteConfigInput }>(UPDATE_REMOTE_CONFIG);

  const onClickConnection = ({ id, status }: Pick<ComputePlatform, 'id' | 'status'>) => {
    if (status === StatusType.Success) {
      clearStore();
      selectProxyID(id);
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
      onClickConnection={(id) => onClickConnection({ id, status: StatusType.Success })}
      deleteConnection={deleteConnection}
      applyConfigurations={applyConfigurations}
    />
  );
}
