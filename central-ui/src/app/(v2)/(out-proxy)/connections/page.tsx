'use client';

import React from 'react';
import { ComputePlatform } from '@/types';
import { StatusType } from '@odigos/ui-kit/types';
import { useOdigos } from '@odigos/ui-kit/contexts';
import { useNotificationStore } from '@odigos/ui-kit/store';
import { CentralConnections } from '@odigos/ui-kit/containers/v2';
import {
  useComputePlatformsCRUD,
  useEffectiveConfig,
  useProxy,
  useProxyCRUD,
  useUpdateRemoteConfig,
  useSnapshots,
  useActionCRUD,
  useInstrumentationRuleCRUD,
  useDestinationCategories,
  usePotentialDestinations,
  useTestConnection,
  useDestinationCRUD,
} from '@/hooks';

// TODO: move all fetch logic to an API Context, goal is to remove all clutter and prop-drilling

export default function Page() {
  const { isVm } = useOdigos();
  const { computePlatforms, getComputePlatforms } = useComputePlatformsCRUD();

  const { selectProxy } = useProxy();
  const { deleteProxy } = useProxyCRUD();
  const { addNotification } = useNotificationStore();

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

  const { testConnection } = useTestConnection(computePlatforms);
  const { getDestinationCategories } = useDestinationCategories(computePlatforms);
  const { getPotentialDestinations } = usePotentialDestinations(computePlatforms);
  const { createDestination, updateDestination } = useDestinationCRUD(computePlatforms);

  const { createAction } = useActionCRUD(computePlatforms);
  const { getEffectiveConfig } = useEffectiveConfig(computePlatforms);
  const { updateRemoteConfigs } = useUpdateRemoteConfig(computePlatforms);
  const { createInstrumentationRule } = useInstrumentationRuleCRUD(computePlatforms);
  const { getAllClusterSnapshots, bulkPersistSources } = useSnapshots(computePlatforms);

  return (
    <CentralConnections
      connections={computePlatforms.length ? computePlatforms : undefined}
      getConnections={getComputePlatforms}
      onClickConnection={onClickConnection}
      deleteConnection={deleteConnection}
      configMinSupportedVersion={1.16}
      getEffectiveConfig={isVm ? undefined : getEffectiveConfig}
      applyConfigurations={isVm ? undefined : updateRemoteConfigs}
      snapshotMinSupportedVersion={1.2}
      getAllClusterSnapshots={isVm ? undefined : getAllClusterSnapshots}
      persistSources={isVm ? undefined : bulkPersistSources}
      createAction={isVm ? undefined : createAction}
      createInstrumentationRule={isVm ? undefined : createInstrumentationRule}
      getDestinationCategories={isVm ? undefined : getDestinationCategories}
      getPotentialDestinations={isVm ? undefined : getPotentialDestinations}
      testConnection={isVm ? undefined : testConnection}
      createDestination={isVm ? undefined : createDestination}
      updateDestination={isVm ? undefined : updateDestination}
    />
  );
}
