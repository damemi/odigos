'use client';

import React from 'react';
import { useOdigos } from '@odigos/ui-kit/contexts';
import { useNotificationStore } from '@odigos/ui-kit/store';
import { ComputePlatform, RemoteConfigInput } from '@/types';
import { CentralConnections } from '@odigos/ui-kit/containers/v2';
import { GET_EFFECTIVE_CONFIG, UPDATE_REMOTE_CONFIG } from '@/graphql';
import { type EffectiveConfig, StatusType, type ApplyConfigurationsFunc } from '@odigos/ui-kit/types';
import { useComputePlatformsCRUD, useProxy, useProxyCRUD, useRemoteFetch, useSnapshots } from '@/hooks';

// TODO: move all fetch logic to an API Context, goal is to remove all clutter and prop-drilling

export default function Page() {
  const { isVm } = useOdigos();
  const { computePlatforms, getComputePlatforms } = useComputePlatformsCRUD();

  const { selectProxy } = useProxy();
  const { deleteProxy } = useProxyCRUD();
  const { addNotification } = useNotificationStore();
  // const { createAction } = useActionCRUD(computePlatforms);
  // const { createInstrumentationRule } = useInstrumentationRuleCRUD(computePlatforms);
  const { getAllClusterSnapshots, bulkPersistSources } = useSnapshots(computePlatforms);

  const { fetch: fetchEffectiveConfig } = useRemoteFetch<{ effectiveConfig?: EffectiveConfig }, void>(GET_EFFECTIVE_CONFIG, computePlatforms);
  const { fetchMulti: updateRemoteConfig } = useRemoteFetch<void, { config: RemoteConfigInput }>(UPDATE_REMOTE_CONFIG, computePlatforms);

  const onClickConnection = ({ id, status }: ComputePlatform) => {
    if (status === StatusType.Success) {
      selectProxy(id);
    } else {
      addNotification({ type: StatusType.Warning, title: 'Connection lost', message: 'Cannot connect to the platform', hideFromHistory: true });
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

  const applyConfigurations: ApplyConfigurationsFunc = (formData, ids) => {
    return updateRemoteConfig(ids, { config: formData });
  };

  return (
    <CentralConnections
      tableRowsMaxHeight={'calc(100vh - 180px)'}
      connections={computePlatforms.length ? computePlatforms : undefined}
      getConnections={getComputePlatforms}
      onClickConnection={onClickConnection}
      deleteConnection={deleteConnection}
      configMinSupportedVersion={1.16}
      getEffectiveConfig={isVm ? undefined : getEffectiveConfig}
      applyConfigurations={isVm ? undefined : applyConfigurations}
      snapshotMinSupportedVersion={1.2}
      getAllClusterSnapshots={isVm ? undefined : getAllClusterSnapshots}
      persistSources={isVm ? undefined : bulkPersistSources}
      // TODO: wait for test-connection, new "selected" area, and for create func type update - then add these props
      // getDestinationCategories={getDestinationCategories}
      // getPotentialDestinations={getPotentialDestinations}
      // createDestination={createDestination}
      // TODO: uncomment these when ready release the features
      // createAction={isVm ? undefined : createAction}
      // createInstrumentationRule={isVm ? undefined : createInstrumentationRule}
    />
  );
}
