import { useEffect } from 'react';
import { GET_CONFIG_YAMLS } from '@/graphql';
import { useRemoteFetch } from '../proxy';
import type { ConfigYaml } from '@odigos/ui-kit/types';

interface FetchedConfigYamls {
  configYamls: ConfigYaml[];
}

export const useConfigYamls = () => {
  const { fetch, data, loading, called } = useRemoteFetch<FetchedConfigYamls, void>(GET_CONFIG_YAMLS);

  useEffect(() => {
    if (!called) fetch();
  }, [called, fetch]);

  return {
    configYamls: data?.configYamls || [],
    configYamlsLoading: loading,
  };
};
