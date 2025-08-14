import { useEffect } from 'react';
import { ROUTES } from '@/utils';
import { StatusType } from '@odigos/ui-kit/types';
import { useEntityStore } from '@odigos/ui-kit/store';
import { useParams, useRouter } from 'next/navigation';
import { useComputePlatformsCRUD } from '../compute-platforms';

interface UseProxyID {
  proxyID: string;
  goSelectNewProxy: () => void;
  selectProxyID: (id: string) => void;
}

export const useProxyID = (): UseProxyID => {
  const router = useRouter();
  const { proxyID } = useParams<{ proxyID: string }>();

  const { computePlatforms } = useComputePlatformsCRUD();
  const { resetEntityStore } = useEntityStore();

  // Reset proxyID if the connection is lost
  useEffect(() => {
    if (proxyID) {
      const foundConnection = computePlatforms.find(({ id }) => id === proxyID);
      const connectionNotFound = computePlatforms.length && !foundConnection;
      const connectionHasError = foundConnection?.status === StatusType.Error;

      if (connectionNotFound || connectionHasError) {
        router.push(ROUTES.CONNECTIONS);
      }
    }
  }, [proxyID, computePlatforms, router]);

  const goSelectNewProxy = () => {
    router.push(ROUTES.CONNECTIONS);
  };

  const selectProxyID = (id: string) => {
    if (id !== proxyID) {
      resetEntityStore();
      router.push(ROUTES.OVERVIEW(id));
    }
  };

  return {
    proxyID,
    goSelectNewProxy,
    selectProxyID,
  };
};
