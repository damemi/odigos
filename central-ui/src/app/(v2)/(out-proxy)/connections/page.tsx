'use client';

import React, { useCallback } from 'react';
import { useProxy } from '@/hooks';
import { useNotificationStore } from '@odigos/ui-kit/store';
import { CentralConnections } from '@odigos/ui-kit/containers';
import { StatusType, type Connection } from '@odigos/ui-kit/types';

// `<CentralConnections>` is the OUT-OF-PROXY entry point: this page lists
// all connected proxy clusters and lets the user pick one. Connection
// list / create / delete run through `useOdigosApi().connectionsApi`
// (wired by the host adapter as bare central-backend ops). This page
// only supplies host-specific navigation (`selectProxy`) and version
// gates for bulk drawers.
//
// Apollo cache for the previous proxy is cleared by `useProxy` when this
// (out-of-proxy) page mounts — see `clearProxy` / `resetCache` (PLAT-1265).
//
// Once the user picks a cluster, `selectProxy(id)` updates the proxy store
// and the next page load (in-proxy routes) sees the right
// `proxyID`/`platformType`/`version` in the operation context.
export default function Page() {
  const { selectProxy } = useProxy();
  const { addNotification } = useNotificationStore();

  const onClickConnection = useCallback(
    (conn: Connection) => {
      if (conn.status === StatusType.Success) {
        // Pass the row's Connection as fallback metadata so selectProxy does
        // not silently no-op if the useComputePlatformsCRUD observer is empty.
        selectProxy(conn.id, conn);
      } else {
        addNotification({ type: StatusType.Warning, title: 'Connection lost', message: 'Cannot connect to the platform', hideFromHistory: true });
      }
    },
    [selectProxy, addNotification],
  );

  return <CentralConnections onClickConnection={onClickConnection} configMinSupportedVersion='1.16' snapshotMinSupportedVersion='1.20' />;
}
