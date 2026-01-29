import { usePathname, useRouter } from 'next/navigation';
import { useEffect } from 'react';
import { ROUTES } from '@/utils';
import { useProxyStore } from '@/store';
import { useComputePlatformsCRUD } from '../compute-platforms';
import { PlatformType, StatusType } from '@odigos/ui-kit/types';
import { ProgressKeys, useDataStreamStore, useEntityStore, useProgressStore, useSetupStore } from '@odigos/ui-kit/store';

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

  const { resetProgress } = useProgressStore();
  const { resetEntityStore } = useEntityStore();
  const { resetDataStreamStore } = useDataStreamStore();
  const { computePlatforms } = useComputePlatformsCRUD();
  const { clearStore: resetSetupStore } = useSetupStore();
  const { proxyID, proxyName, odigosVersion, platformType, setProxyStore, resetProxyStore } = useProxyStore();

  const resetStores = () => {
    resetSetupStore();
    resetEntityStore();
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

  // Reset proxyID if the pathname is connections
  useEffect(() => {
    if (pathname === ROUTES.CONNECTIONS) clearProxy({ skipRedirect: true });
  }, [pathname]);

  return {
    proxyID,
    proxyName,
    odigosVersion,
    platformType,
    selectProxy,
    clearProxy,
  };
};
