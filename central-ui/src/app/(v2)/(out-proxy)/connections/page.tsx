'use client';

import React, { useCallback, useEffect, useState } from 'react';
import { useProxy } from '@/hooks';
import type { ComputePlatform } from '@/types';
import { useNotificationStore } from '@odigos/ui-kit/store';
import { CentralConnections } from '@odigos/ui-kit/containers';
import { StatusType, type Connection } from '@odigos/ui-kit/types';
import { useApolloClient, useMutation, useQuery } from '@apollo/client/react';
import { GET_COMPUTE_PLATFORMS, DELETE_PROXY_CONNECTION } from '@/lib/odigos-api-adapter';

// `<CentralConnections>` is the OUT-OF-PROXY entry point: this page lists
// all connected proxy clusters and lets the user pick one. Cluster CRUD
// operations (delete, "manage") are handled here directly via Apollo —
// they don't go through `useOdigosApi()` because they aren't per-proxy
// CRUD; they're meta-operations on the connections themselves.
//
// Once the user picks a cluster, `selectProxy(id)` updates the proxy store
// and the next page load (in-proxy routes) sees the right
// `proxyID`/`platformType`/`version` in the operation context.
export default function Page() {
  const apollo = useApolloClient();
  const { selectProxy } = useProxy();
  const { addNotification } = useNotificationStore();

  const [connections, setConnections] = useState<Connection[]>([]);

  const { data, refetch } = useQuery<{ computePlatforms?: ComputePlatform[] }>(GET_COMPUTE_PLATFORMS);
  const [mutateDelete] = useMutation<{ deleteProxyConnection: boolean }, { id: string }>(DELETE_PROXY_CONNECTION);

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
      await mutateDelete({ variables: { id } });
      // Refresh the local list immediately; the kit's CentralConnections
      // also calls getConnections() after delete, so this is belt-and-suspenders.
      await refetch();
    },
    [mutateDelete, refetch],
  );

  // Suppress unused — kept for future direct Apollo escape hatches.
  void apollo;

  return (
    <CentralConnections
      connections={connections}
      getConnections={getConnections}
      // eslint-disable-next-line @typescript-eslint/no-explicit-any
      onClickConnection={onClickConnection as any}
      deleteConnection={deleteConnection}
      configMinSupportedVersion={1.16}
      snapshotMinSupportedVersion={1.2}
    />
  );
}
