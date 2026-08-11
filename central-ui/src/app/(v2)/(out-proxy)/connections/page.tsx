'use client';

import React, { useCallback, useEffect, useState } from 'react';
import type { ComputePlatform } from '@/types';
import { useProxy, useProxyCRUD } from '@/hooks';
import * as STRINGS from '@/utils/constants/strings';
import { useNotificationStore } from '@odigos/ui-kit/store';
import { useMutation, useQuery } from '@apollo/client/react';
import { GET_COMPUTE_PLATFORMS } from '@/lib/odigos-api-adapter';
import { CentralConnections, CreateNewType } from '@odigos/ui-kit/containers';
import { GET_CLOUD_CONNECTOR_PROVIDERS, CREATE_CLOUD_CONNECTOR } from '@/graphql';
import { StatusType, type CloudConnectorCreateInput, type CloudConnectorCreateResult, type CloudConnectorProvider, type Connection } from '@odigos/ui-kit/types';

// `<CentralConnections>` is the OUT-OF-PROXY entry point: this page lists
// all connected proxy clusters and lets the user pick one. Cluster CRUD
// operations (delete, "manage") are handled here directly via Apollo —
// they don't go through `useOdigosApi()` because they aren't per-proxy
// CRUD; they're meta-operations on the connections themselves.
//
// Apollo cache for the previous proxy is cleared by `useProxy` when this
// (out-of-proxy) page mounts — see `clearProxy` / `resetCache` (PLAT-1265).
//
// Once the user picks a cluster, `selectProxy(id)` updates the proxy store
// and the next page load (in-proxy routes) sees the right
// `proxyID`/`platformType`/`version` in the operation context.
export default function Page() {
  const { selectProxy } = useProxy();
  const { deleteConnection: deleteConnectionByType } = useProxyCRUD();
  const { addNotification } = useNotificationStore();

  const [connections, setConnections] = useState<Connection[]>([]);

  const { data, refetch } = useQuery<{ computePlatforms?: ComputePlatform[] }>(GET_COMPUTE_PLATFORMS);
  const { data: providersData } = useQuery<{ cloudConnectorProviders?: CloudConnectorProvider[] }>(GET_CLOUD_CONNECTOR_PROVIDERS);
  const [mutateCreateCloudConnector] = useMutation<{ createCloudConnector: CloudConnectorCreateResult }, { input: CloudConnectorCreateInput }>(CREATE_CLOUD_CONNECTOR);

  useEffect(() => {
    if (data?.computePlatforms) {
      setConnections(data.computePlatforms as unknown as Connection[]);
    }
  }, [data]);

  const getConnections = useCallback(async (): Promise<Connection[]> => {
    const r = await refetch();
    return ((r.data?.computePlatforms ?? []) as unknown as Connection[]) || [];
  }, [refetch]);

  const onClickConnection = useCallback(
    ({ id, status }: { id: string; status: StatusType }) => {
      if (status === StatusType.Success) {
        selectProxy(id);
      } else {
        addNotification({ type: StatusType.Warning, title: 'Connection lost', message: 'Cannot connect to the platform', hideFromHistory: true });
      }
    },
    [selectProxy, addNotification],
  );

  const deleteConnection = useCallback(
    async (id: string) => {
      const type = connections.find((c) => c.id === id)?.type;
      const result = await deleteConnectionByType(id, type);
      if (!result?.success) return;
      // Refresh the local list immediately; the kit's CentralConnections
      // also calls getConnections() after delete, so this is belt-and-suspenders.
      await refetch();
    },
    [connections, deleteConnectionByType, refetch],
  );

  // Verifies and creates the cloud connector in one server call. On success the
  // backend persists the credentials Secret + OdigosCloudConnector CR; the drawer
  // then triggers a connections refetch so the new connector appears in the list.
  const onCreateCloudConnector = useCallback(
    async (input: CloudConnectorCreateInput): Promise<CloudConnectorCreateResult> => {
      const { data } = await mutateCreateCloudConnector({ variables: { input } });
      if (!data?.createCloudConnector) {
        throw new Error('No result returned from create connector');
      }
      return data.createCloudConnector;
    },
    [mutateCreateCloudConnector],
  );

  // Handles non-connector "Create new" selections. Cloud Connector is handled
  // inside CentralConnections (opens the create drawer). Only Cloud Connector
  // is exposed in the dropdown for now.
  const onCreateNew = useCallback(
    (type: CreateNewType) => {
      addNotification({ type: StatusType.Info, title: STRINGS.CREATE_NEW, message: STRINGS.CREATE_NEW_COMING_SOON(type), hideFromHistory: true });
    },
    [addNotification],
  );

  return (
    <CentralConnections
      connections={connections}
      getConnections={getConnections}
      // eslint-disable-next-line @typescript-eslint/no-explicit-any
      onClickConnection={onClickConnection as any}
      deleteConnection={deleteConnection}
      onCreateNew={onCreateNew}
      cloudConnectorProviders={providersData?.cloudConnectorProviders ?? []}
      onCreateCloudConnector={onCreateCloudConnector}
      configMinSupportedVersion={1.16}
    />
  );
}
