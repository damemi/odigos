import { usePathname, useRouter } from 'next/navigation';
import { useEffect } from 'react';
import { ROUTES } from '@/utils';
import { useProxyStore } from '@/store';
import { useComputePlatformsCRUD } from '../compute-platforms';
import { PlatformType, StatusType } from '@odigos/ui-kit/types';
import { useEntityStore, useInstrumentStore, useSetupStore } from '@odigos/ui-kit/store';

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

  const { resetEntityStore } = useEntityStore();
  const { clearStore: resetSetupStore } = useSetupStore();
  const { setInstrumentAwait, setInstrumentCount } = useInstrumentStore();
  const { proxyID, proxyName, odigosVersion, platformType, setProxyStore, resetProxyStore } = useProxyStore();

  const { computePlatforms } = useComputePlatformsCRUD();

  const resetStores = () => {
    resetEntityStore();
    resetSetupStore();
    setInstrumentAwait(false);
    setInstrumentCount('sourcesToCreate', 0);
    setInstrumentCount('sourcesCreated', 0);
    setInstrumentCount('sourcesToDelete', 0);
    setInstrumentCount('sourcesDeleted', 0);
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
