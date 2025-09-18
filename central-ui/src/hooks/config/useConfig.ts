import { useEffect } from 'react';
import { useProxy } from '../proxy';
import { GET_CONFIG } from '@/graphql';
import { useUserStore } from '@/store';
import { Role, type FetchedConfig } from '@/types';
import { InstallationMethod, Tier } from '@odigos/ui-kit/types';

export const useConfig = () => {
  const { user } = useUserStore();
  const { data, fetch } = useProxy<{ config?: FetchedConfig }>(GET_CONFIG);

  useEffect(() => {
    if (!data?.config) fetch();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  const config = data?.config;
  const isReadonly = user?.role === Role.VIEWER || data?.config?.readonly || false;
  const installationMethod = data?.config?.installationMethod || InstallationMethod.Cli;
  const isCommunity = (config?.tier && [Tier.Community].includes(config.tier)) || false;
  const isEnterprise = (config?.tier && [Tier.Onprem].includes(config.tier)) || false;

  return {
    config,
    isReadonly,
    installationMethod,
    isCommunity,
    isEnterprise,
  };
};
