import { usePathname, useRouter } from 'next/navigation';
import { useEffect, useMemo } from 'react';
import { ROUTES } from '@/utils';
import { useProxyStore } from '@/store';
import { useComputePlatformsCRUD } from '../compute-platforms';
import { PlatformType, StatusType } from '@odigos/ui-kit/types';
import { useOdigosApi } from '@odigos/ui-kit/contexts/odigos-api';
import { ProgressKeys, useDataStreamStore, useProgressStore, useSetupStore } from '@odigos/ui-kit/store';

interface UseProxy {
  proxyID?: string;
  proxyName?: string;
  odigosVersion?: string;
  platformType?: PlatformType;
  selectProxy: (id: string) => void;
  clearProxy: () => void;
}

export const useProxy = (): UseProxy => {
  const router = useRouter();
  const pathname = usePathname();

  const { resetCache } = useOdigosApi();
  const { resetProgress } = useProgressStore();
  const { resetDataStreamStore } = useDataStreamStore();
  const { clearStore: resetSetupStore } = useSetupStore();
  const { proxyID: storedProxyID, proxyName, odigosVersion, platformType, setProxyStore, resetProxyStore } = useProxyStore();

  const { computePlatforms } = useComputePlatformsCRUD();

  // On out-of-proxy pages, treat proxyID as undefined immediately (before effects run)
  // This prevents stale proxyID from triggering fetches before the cleanup effect runs
  const isOutOfProxyPage = useMemo(() => [ROUTES.ROOT, ROUTES.CONNECTIONS, ROUTES.ACCESS_MANAGEMENT, ROUTES.SIGN_IN, ROUTES.UPDATE_YOUR_PASSWORD].includes(pathname), [pathname]);
  const proxyID = isOutOfProxyPage ? undefined : storedProxyID;

  const resetStores = () => {
    resetSetupStore();
    // Drop all entity data cached against the previous proxy; the
    // kit's domain hooks will re-fetch on next render under the
    // new proxyID. (Not awaited — `clearStore` is fire-and-forget.)
    resetCache();
    resetDataStreamStore();
    resetProgress(ProgressKeys.Instrumenting);
    resetProgress(ProgressKeys.Uninstrumenting);
  };

  const selectProxy = (id: string) => {
    const found = computePlatforms.find((cp) => cp.id === id);
    if (found) {
      resetStores();
      setProxyStore({
        proxyID: id,
        proxyName: found?.name,
        odigosVersion: found?.odigosVersion,
        platformType: found?.type,
      });

      router.push(ROUTES.OVERVIEW);
    }
  };

  const clearProxy = (opts?: { skipRedirect?: boolean }) => {
    resetStores();
    resetProxyStore();
    if (!opts?.skipRedirect) router.push(ROUTES.CONNECTIONS);
  };

  // Reset proxyID if the connection is lost
  useEffect(() => {
    if (proxyID) {
      const found = computePlatforms.find((cp) => cp.id === proxyID);
      const connectionNotFound = computePlatforms.length && !found;
      const connectionHasError = found?.status === StatusType.Error;
      if (connectionNotFound || connectionHasError) clearProxy();
    }
  }, [proxyID, computePlatforms.length]);

  // Clear the store when on out-of-proxy pages (cleanup for next navigation)
  // Or navigate to the connections page if the proxyID is undefined
  useEffect(() => {
    if (isOutOfProxyPage) clearProxy({ skipRedirect: true });
    else if (!isOutOfProxyPage && !proxyID) clearProxy();
  }, [isOutOfProxyPage, proxyID]);

  return {
    proxyID,
    proxyName,
    odigosVersion,
    platformType,
    selectProxy,
    clearProxy,
  };
};
